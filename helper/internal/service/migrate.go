package service

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/chrisbanes/portico/helper/internal/installation"
)

// Layout under the caller-supplied state root. Originals (installation-v5.json
// … portal-v1.json and tsnet/<uuid>/) are only ever read.
const (
	serviceDir     = "service"
	manifestFile   = "service/manifest.json"
	generationsDir = "service/generations"
	checkpointsDir = "service/checkpoints"
	stagingDir     = "service/staging"
	identitiesDir  = "tsnet"

	// The MIG-1 guard's transaction record: a checkpoint of each guarded
	// path and the source (copies plus checkpoint.json), then the intent
	// naming the checkpoint's hash. They are kept after the commit for
	// manual recovery.
	guardDir            = "service/guard"
	guardCopiesDir      = "service/guard/originals"
	guardCheckpointFile = "service/guard/checkpoint.json"
	guardIntentFile     = "service/guard/intent.json"
	migrationFailedFile = "service/migration-failed.json"
	guardSchema         = 1

	manifestSchema = 1
	phaseCommitted = "committed"
	stateFileName  = "state.json"

	// freeSpaceMargin is headroom beyond the copies for the state document
	// and manifest.
	freeSpaceMargin = 1 << 20
)

var generationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// guardTargets are the records every released reader loads first: v0.0.x
// reads installation-v4.json and main reads installation-v5.json.
var guardTargets = [...]string{"installation-v5.json", "installation-v4.json"}

// guardBytes is an unsupported-version record no reader accepts. Readers
// still save over a plain file, so it is also made immutable (uchg).
var guardBytes = []byte(`{"version":2147483647,"porticoServiceGuard":1}`)

// Phase names a migration boundary passed to Options.Hook.
type Phase string

const (
	PhaseGuardCheckpoint Phase = "guardCheckpoint"
	PhaseGuardIntent     Phase = "guardIntent"
	PhaseGuardInstall    Phase = "guardInstall"
	PhaseGuardFlag       Phase = "guardFlag"
	PhaseCopy            Phase = "copy"
	PhaseWrite           Phase = "write"
	PhaseFsync           Phase = "fsync"
	PhaseBeforeManifest  Phase = "beforeManifest"
	PhaseAfterManifest   Phase = "afterManifest"
	PhaseCommitWrite     Phase = "commitWrite"
	PhaseCommitRename    Phase = "commitRename"
	PhaseCommitFsync     Phase = "commitFsync"
)

type Options struct {
	// Hook, when set, runs before each phase; an error aborts there as if
	// that filesystem operation had failed. Tests use it to inject faults.
	Hook func(Phase) error
	// LegacyGuard installs and then requires the MIG-1 guard, which keeps
	// older Portico apps from loading or saving this root. Only macOS has
	// such readers; elsewhere Open refuses it.
	LegacyGuard bool
}

func (o Options) hook(p Phase) error {
	if o.Hook == nil {
		return nil
	}
	if err := o.Hook(p); err != nil {
		return persistenceErr(err)
	}
	return nil
}

type guardCheckpoint struct {
	Schema int `json:"schema"`
	// Source is the authoritative record imported; empty for a fresh root.
	Source string `json:"source"`
	// Files holds each guard target and the source as they were.
	Files map[string]guardOriginal `json:"files"`
}

type guardOriginal struct {
	Present bool   `json:"present"`
	Mode    uint32 `json:"mode,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

type guardIntent struct {
	Schema     int    `json:"schema"`
	Checkpoint string `json:"checkpoint"`
}

type manifest struct {
	Schema     int               `json:"schema"`
	Generation string            `json:"generation"`
	Phase      string            `json:"phase"`
	Sources    map[string]string `json:"sources"`
}

// Owner is exclusive lifetime ownership of a state root. A host keeps it even
// when the state cannot load, so no second service starts against a root it
// is reporting as failed.
type Owner struct {
	root *os.Root
	lock *os.File
	path string
}

// Acquire takes the lifetime lock on the state root, failing with
// ErrOwnershipBusy while another service holds it.
func Acquire(rootPath string) (*Owner, error) {
	rootPath, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, persistenceErr(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return nil, persistenceErr(err)
	}
	if info, err := root.Stat("."); err != nil || checkOwned(info) != nil {
		root.Close()
		return nil, unsafeErr(".")
	}
	lock, err := lockOwner(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &Owner{root: root, lock: lock, path: rootPath}, nil
}

// LockFile is the held lifetime lock, for ipc.Listen.
func (o *Owner) LockFile() *os.File { return o.lock }

// Close releases ownership.
func (o *Owner) Close() error {
	return errors.Join(o.lock.Close(), o.root.Close())
}

// Open loads the root's committed generation, or imports the historical
// installation into a new one, which commits only when its manifest does.
// Call it once. On success the Store takes over ownership and Store.Close
// releases it; on failure ownership stays with o.
//
// With Options.LegacyGuard the import first guards the records older apps
// read (MIG-1), and every Open verifies that guard before returning.
func (o *Owner) Open(opts Options) (*Store, error) {
	s := &Store{root: o.root, lock: o.lock, opts: opts}
	if err := s.open(); err != nil {
		return nil, err
	}
	s.identityRoot = filepath.Join(o.path, filepath.FromSlash(s.generation), identitiesDir)
	return s, nil
}

// Open takes ownership of the state root and opens its store, releasing
// ownership again if that fails.
func Open(rootPath string, opts Options) (*Store, error) {
	owner, err := Acquire(rootPath)
	if err != nil {
		return nil, err
	}
	s, err := owner.Open(opts)
	if err != nil {
		owner.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) open() error {
	// os.Root follows symlinks that stay inside the root, so a service
	// directory linked into the originals would redirect every write there.
	for _, dir := range []string{serviceDir, generationsDir, checkpointsDir, stagingDir, guardDir, guardCopiesDir} {
		if info, err := s.root.Lstat(dir); err == nil && (!info.IsDir() || checkOwned(info) != nil) {
			return unsafeErr(dir)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return persistenceErr(err)
		}
	}
	if s.opts.LegacyGuard {
		if err := guardSupported(); err != nil {
			return err
		}
	}
	// After a terminal guarded-import failure only manual recovery may
	// guard the root again.
	if _, err := s.root.Lstat(migrationFailedFile); err == nil {
		return ErrMigrationFailed
	} else if !errors.Is(err, fs.ErrNotExist) {
		return persistenceErr(err)
	}
	if _, err := s.root.Lstat(manifestFile); errors.Is(err, fs.ErrNotExist) {
		// The manifest is the commit point, and generations appear only after
		// it, so committed data without a manifest is damage, not an
		// interrupted import.
		for _, dir := range []string{generationsDir, checkpointsDir} {
			entries, err := readDir(s.root, dir)
			if err != nil {
				return persistenceErr(err)
			}
			if len(entries) > 0 {
				return unsupportedErr(fmt.Errorf("%s without a manifest", dir))
			}
		}
		if err := s.root.RemoveAll(stagingDir); err != nil {
			return persistenceErr(err)
		}
		if err := s.migrate(); err != nil {
			return err
		}
	} else if err != nil {
		return persistenceErr(err)
	}
	// Checked on every start, before the runtime can: nothing re-guards
	// automatically after the commit.
	if s.opts.LegacyGuard {
		if err := verifyGuard(s.root); err != nil {
			return err
		}
	}
	return s.load()
}

func (s *Store) migrate() (err error) {
	// A recorded guard intent means an earlier attempt may have guarded the
	// originals, so it rolls forward from the verified checkpoint copies.
	var recorded *guardCheckpoint
	importRoot := s.root
	if s.opts.LegacyGuard {
		if recorded, err = s.resumeGuard(); err != nil {
			return err
		}
		if recorded != nil {
			if importRoot, err = s.root.OpenRoot(guardCopiesDir); err != nil {
				return persistenceErr(err)
			}
			defer importRoot.Close()
		}
	}
	imported, source, err := installation.Import(importRoot)
	if err != nil {
		var importErr *installation.Error
		if errors.As(err, &importErr) && importErr.Kind == installation.ErrUnreadable {
			return persistenceErr(err)
		}
		return unsupportedErr(err)
	}
	if source.File != "" {
		if info, err := importRoot.Lstat(source.File); err != nil || checkOwned(info) != nil {
			return unsafeErr(source.File)
		}
	}
	if recorded != nil && (source.File != recorded.Source || (source.File != "" && hash(source.Data) != recorded.Files[source.File].SHA256)) {
		return unsupportedErr(errors.New("guard checkpoint does not match its source"))
	}
	identities, err := mapIdentities(s.root, imported)
	if err != nil {
		return err
	}
	// Hash every original first: this also rejects unsafe entries before
	// anything is written, and sizes the free-space check.
	sources := map[string]string{}
	originals := map[string]map[string]string{}
	// The checkpoint keeps the source bytes; the state document is a small
	// multiple of them, and the margin absorbs the difference.
	need := 2*int64(len(source.Data)) + freeSpaceMargin
	if source.File != "" {
		sources[source.File] = hash(source.Data)
	}
	for original, portalID := range identities {
		sums, size, err := hashTree(s.root, path.Join(identitiesDir, original))
		if err != nil {
			return err
		}
		originals[original] = sums
		need += 2 * size // checkpoint and working copy
		for rel, sum := range sums {
			sources[path.Join(identitiesDir, portalID, rel)] = sum
		}
	}
	if err := checkFreeSpace(s.lock, need); err != nil {
		return err
	}

	if s.opts.LegacyGuard {
		if recorded == nil {
			if recorded, err = s.checkpointGuard(source); err != nil {
				return err
			}
		}
		// From the intent on, an interruption rolls forward and keeps the
		// guard; only a failure no retry can fix restores the originals.
		defer func() {
			if err == nil || errors.Is(err, ErrPersistenceUnavailable) {
				return
			}
			if _, statErr := s.root.Lstat(manifestFile); errors.Is(statErr, fs.ErrNotExist) {
				err = errors.Join(ErrMigrationFailed, s.failMigration(recorded), err)
			}
		}()
		if err := s.installGuard(recorded); err != nil {
			return err
		}
	}

	id, err := newGenerationID()
	if err != nil {
		return persistenceErr(err)
	}
	staged := path.Join(stagingDir, id)
	defer func() {
		// Free the space now, unless the manifest committed this generation
		// (it is then rolled forward on the next Open). Best effort: the
		// next Open discards uncommitted staging anyway.
		if err == nil {
			return
		}
		if _, statErr := s.root.Lstat(manifestFile); errors.Is(statErr, fs.ErrNotExist) {
			_ = s.root.RemoveAll(staged)
		}
	}()
	generation, checkpoint := path.Join(staged, "generation"), path.Join(staged, "checkpoint")
	for _, dir := range []string{serviceDir, stagingDir, staged, generation, checkpoint, path.Join(generation, identitiesDir), path.Join(checkpoint, identitiesDir)} {
		if err := mkdir(s.root, dir); err != nil {
			return err
		}
	}
	for _, dir := range []string{".", serviceDir} {
		if err := syncDir(s.root, dir); err != nil {
			return err
		}
	}

	if err := s.opts.hook(PhaseCopy); err != nil {
		return err
	}
	if source.File != "" {
		if err := writeFileSync(s.root, path.Join(checkpoint, source.File), source.Data); err != nil {
			return err
		}
	}
	for original, portalID := range identities {
		copies := []string{path.Join(checkpoint, identitiesDir, portalID), path.Join(generation, identitiesDir, portalID)}
		if err := copyTree(s.root, path.Join(identitiesDir, original), copies); err != nil {
			return err
		}
		for _, dir := range copies {
			got, _, err := hashTree(s.root, dir)
			if err != nil {
				return err
			}
			if !maps.Equal(got, originals[original]) {
				return persistenceErr(fmt.Errorf("identity %s changed while copying", portalID))
			}
		}
	}

	if err := s.opts.hook(PhaseWrite); err != nil {
		return err
	}
	state := State{
		Schema:       stateSchema,
		Installation: imported,
		Migration:    Migration{Source: source.File, SourceVersion: source.Version},
	}
	data, err := encodeState(state)
	if err != nil {
		return err
	}
	if err := writeFileSync(s.root, path.Join(generation, stateFileName), data); err != nil {
		return err
	}

	if err := s.opts.hook(PhaseFsync); err != nil {
		return err
	}
	if err := syncTree(s.root, staged); err != nil {
		return err
	}

	if err := s.opts.hook(PhaseBeforeManifest); err != nil {
		return err
	}
	m, err := json.Marshal(manifest{Schema: manifestSchema, Generation: id, Phase: phaseCommitted, Sources: sources})
	if err != nil {
		return persistenceErr(err)
	}
	if err := replaceFile(s.root, manifestFile, m); err != nil {
		return err
	}
	return s.opts.hook(PhaseAfterManifest)
}

// resumeGuard returns the verified checkpoint of an earlier guarded import
// that recorded its intent, or nil when there is none. A guard without a
// verified checkpoint fails closed, touching nothing: the checkpoint holds
// the only copy of the originals it replaced.
func (s *Store) resumeGuard() (*guardCheckpoint, error) {
	if _, err := s.root.Lstat(guardIntentFile); errors.Is(err, fs.ErrNotExist) {
		for _, name := range guardTargets {
			if guarded, err := isGuard(s.root, name); err != nil {
				return nil, err
			} else if guarded {
				return nil, unsupportedErr(errors.New("legacy guard without a checkpoint"))
			}
		}
		// Only an incomplete checkpoint from before the intent can be here.
		if err := s.root.RemoveAll(guardDir); err != nil {
			return nil, persistenceErr(err)
		}
		return nil, nil
	} else if err != nil {
		return nil, persistenceErr(err)
	}
	data, err := readRegular(s.root, guardIntentFile)
	if err != nil {
		return nil, err
	}
	var intent guardIntent
	if err := decodeStrict(data, &intent); err != nil || intent.Schema != guardSchema {
		return nil, unsupportedErr(errors.New("unsupported guard intent"))
	}
	data, err = readRegular(s.root, guardCheckpointFile)
	if err != nil {
		return nil, err
	}
	var checkpoint guardCheckpoint
	if hash(data) != intent.Checkpoint || decodeStrict(data, &checkpoint) != nil || checkpoint.Schema != guardSchema {
		return nil, unsupportedErr(errors.New("guard checkpoint does not match its intent"))
	}
	names := guardNames(checkpoint.Source)
	if len(checkpoint.Files) != len(names) || (checkpoint.Source != "" && !checkpoint.Files[checkpoint.Source].Present) {
		return nil, unsupportedErr(errors.New("unsupported guard checkpoint"))
	}
	for _, name := range names {
		original, ok := checkpoint.Files[name]
		if !ok {
			return nil, unsupportedErr(errors.New("unsupported guard checkpoint"))
		}
		copied := path.Join(guardCopiesDir, name)
		if !original.Present {
			if _, err := s.root.Lstat(copied); !errors.Is(err, fs.ErrNotExist) {
				return nil, unsupportedErr(errors.New("unsupported guard checkpoint"))
			}
			continue
		}
		data, err := readRegular(s.root, copied)
		if err != nil {
			return nil, err
		}
		if hash(data) != original.SHA256 {
			return nil, unsupportedErr(fmt.Errorf("guard checkpoint copy of %s changed", name))
		}
	}
	return &checkpoint, nil
}

// guardNames are the paths a guard checkpoint records: both targets, and
// the source when it is an older record.
func guardNames(source string) []string {
	names := guardTargets[:]
	if source != "" && !slices.Contains(names, source) {
		names = append(slices.Clone(names), source)
	}
	return names
}

// checkpointGuard durably records each guarded path's presence, mode and
// bytes, and the source's, then the intent naming that checkpoint's hash.
// It writes nothing outside service/guard.
func (s *Store) checkpointGuard(source installation.Source) (*guardCheckpoint, error) {
	if err := s.opts.hook(PhaseGuardCheckpoint); err != nil {
		return nil, err
	}
	for _, dir := range []string{serviceDir, guardDir, guardCopiesDir} {
		if err := mkdir(s.root, dir); err != nil {
			return nil, err
		}
	}
	checkpoint := guardCheckpoint{Schema: guardSchema, Source: source.File, Files: map[string]guardOriginal{}}
	for _, name := range guardNames(source.File) {
		info, err := s.root.Lstat(name)
		if errors.Is(err, fs.ErrNotExist) {
			checkpoint.Files[name] = guardOriginal{}
			continue
		}
		if err != nil {
			return nil, persistenceErr(err)
		}
		data, err := readRegular(s.root, name)
		if err != nil {
			return nil, err
		}
		if name == source.File && !bytes.Equal(data, source.Data) {
			return nil, persistenceErr(fmt.Errorf("%s changed while importing", name))
		}
		if err := writeFileSync(s.root, path.Join(guardCopiesDir, name), data); err != nil {
			return nil, err
		}
		checkpoint.Files[name] = guardOriginal{Present: true, Mode: uint32(info.Mode().Perm()), SHA256: hash(data)}
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, persistenceErr(err)
	}
	if err := writeFileSync(s.root, guardCheckpointFile, data); err != nil {
		return nil, err
	}
	for _, dir := range []string{guardCopiesDir, guardDir, serviceDir, "."} {
		if err := syncDir(s.root, dir); err != nil {
			return nil, err
		}
	}
	if err := s.opts.hook(PhaseGuardIntent); err != nil {
		return nil, err
	}
	intent, err := json.Marshal(guardIntent{Schema: guardSchema, Checkpoint: hash(data)})
	if err != nil {
		return nil, persistenceErr(err)
	}
	if err := replaceFile(s.root, guardIntentFile, intent); err != nil {
		return nil, err
	}
	return &checkpoint, nil
}

// installGuard puts the guard at each target that does not hold it yet,
// replacing only the checkpointed original, makes it immutable and
// verifies the result.
func (s *Store) installGuard(checkpoint *guardCheckpoint) error {
	for _, name := range guardTargets {
		if err := s.opts.hook(PhaseGuardInstall); err != nil {
			return err
		}
		guarded, err := isGuard(s.root, name)
		if err != nil {
			return err
		}
		if !guarded {
			if err := matchesOriginal(s.root, name, checkpoint.Files[name]); err != nil {
				return err
			}
			if err := replaceFileVia(s.root, path.Join(guardDir, name+".tmp"), name, guardBytes, 0o600); err != nil {
				return err
			}
		}
		if err := s.opts.hook(PhaseGuardFlag); err != nil {
			return err
		}
		if err := setImmutable(s.root, name, true); err != nil {
			return err
		}
	}
	return verifyGuard(s.root)
}

// failMigration ends a guarded import that cannot succeed: it durably marks
// the root failed first, so no later Open guards it again, then restores
// each guarded target's original bytes and mode, or its absence.
func (s *Store) failMigration(checkpoint *guardCheckpoint) error {
	if err := replaceFile(s.root, migrationFailedFile, []byte(`{"schema":1}`)); err != nil {
		return err
	}
	var errs []error
	for _, name := range guardTargets {
		guarded, err := isGuard(s.root, name)
		if err != nil || !guarded {
			// Anything else there is not ours to replace.
			errs = append(errs, err)
			continue
		}
		if err := setImmutable(s.root, name, false); err != nil {
			errs = append(errs, err)
			continue
		}
		original := checkpoint.Files[name]
		if !original.Present {
			if err := s.root.Remove(name); err != nil {
				errs = append(errs, persistenceErr(err))
			}
			errs = append(errs, syncDir(s.root, "."))
			continue
		}
		data, err := readRegular(s.root, path.Join(guardCopiesDir, name))
		if err == nil && hash(data) != original.SHA256 {
			err = unsupportedErr(fmt.Errorf("guard checkpoint copy of %s changed", name))
		}
		if err == nil {
			err = replaceFileVia(s.root, path.Join(guardDir, name+".restore"), name, data, fs.FileMode(original.Mode))
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// isGuard reports whether name is a regular file holding the guard bytes.
func isGuard(root *os.Root, name string) (bool, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, persistenceErr(err)
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(guardBytes)) {
		return false, nil
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return false, persistenceErr(err)
	}
	return bytes.Equal(data, guardBytes), nil
}

// matchesOriginal fails unless name is still as its checkpoint recorded it.
func matchesOriginal(root *os.Root, name string, original guardOriginal) error {
	info, err := root.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist) && !original.Present:
		return nil
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return persistenceErr(err)
	case err == nil && original.Present && info.Mode().IsRegular():
		data, err := root.ReadFile(name)
		if err != nil {
			return persistenceErr(err)
		}
		if hash(data) == original.SHA256 {
			return nil
		}
	}
	return unsupportedErr(fmt.Errorf("%s changed since its guard checkpoint", name))
}

// verifyGuard fails with ErrMigrationGuardMissing unless every target is a
// regular, private, immutable file holding exactly the guard bytes.
func verifyGuard(root *os.Root) error {
	for _, name := range guardTargets {
		info, err := root.Lstat(name)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || checkOwned(info) != nil {
			return ErrMigrationGuardMissing
		}
		file, err := root.Open(name)
		if err != nil {
			return ErrMigrationGuardMissing
		}
		opened, statErr := file.Stat()
		immutable, flagErr := isImmutable(file)
		data, readErr := io.ReadAll(io.LimitReader(file, int64(len(guardBytes))+1))
		file.Close()
		if statErr != nil || flagErr != nil || readErr != nil || !os.SameFile(info, opened) || !immutable || !bytes.Equal(data, guardBytes) {
			return ErrMigrationGuardMissing
		}
	}
	return nil
}

// load opens the committed generation, first finishing the move out of
// staging if a previous Open stopped after committing the manifest.
func (s *Store) load() error {
	data, err := readRegular(s.root, manifestFile)
	if err != nil {
		return err
	}
	var m manifest
	if err := decodeStrict(data, &m); err != nil || m.Schema != manifestSchema || m.Phase != phaseCommitted || !generationIDPattern.MatchString(m.Generation) {
		return unsupportedErr(errors.New("unsupported manifest"))
	}
	staged := path.Join(stagingDir, m.Generation)
	for _, move := range [][2]string{
		{path.Join(staged, "generation"), path.Join(generationsDir, m.Generation)},
		{path.Join(staged, "checkpoint"), path.Join(checkpointsDir, m.Generation)},
	} {
		if _, err := s.root.Lstat(move[1]); err == nil {
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return persistenceErr(err)
		}
		if err := mkdir(s.root, path.Dir(move[1])); err != nil {
			return err
		}
		if err := s.root.Rename(move[0], move[1]); errors.Is(err, fs.ErrNotExist) {
			// The manifest names a generation that exists nowhere.
			return unsupportedErr(err)
		} else if err != nil {
			return persistenceErr(err)
		}
		if err := syncDir(s.root, path.Dir(move[1])); err != nil {
			return err
		}
	}
	if err := syncDir(s.root, serviceDir); err != nil {
		return err
	}
	if err := s.root.RemoveAll(stagingDir); err != nil {
		return persistenceErr(err)
	}
	s.generation = path.Join(generationsDir, m.Generation)
	if info, err := s.root.Lstat(s.generation); err != nil || !info.IsDir() || checkOwned(info) != nil {
		return unsafeErr(s.generation)
	}
	// The runtime will use this tree, so apply the import's safety rules to it
	// again. Contents are not compared: running nodes legitimately change them.
	if err := walkSafe(s.root, path.Join(s.generation, identitiesDir), func(string, fs.FileInfo) error { return nil }); err != nil {
		return err
	}
	data, err = readRegular(s.root, path.Join(s.generation, stateFileName))
	if err != nil {
		return err
	}
	state, err := decodeState(data)
	if err != nil {
		return err
	}
	s.state = state
	return nil
}

// mapIdentities maps each original tsnet/<uuid> directory that belongs to an
// imported Portal to its lowercase UUID. Other children are not copied.
func mapIdentities(root *os.Root, in installation.Installation) (map[string]string, error) {
	info, err := root.Lstat(identitiesDir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, persistenceErr(err)
	}
	if !info.IsDir() || checkOwned(info) != nil {
		return nil, unsafeErr(identitiesDir)
	}
	portals := map[string]bool{}
	for _, p := range in.Portals {
		portals[p.ID] = true
	}
	entries, err := readDir(root, identitiesDir)
	if err != nil {
		return nil, persistenceErr(err)
	}
	mapped := map[string]string{}
	seen := map[string]bool{}
	for _, entry := range entries {
		id := strings.ToLower(entry.Name())
		// Validate proved every Portal ID is a lowercase UUID.
		if !portals[id] {
			continue
		}
		if seen[id] {
			return nil, unsupportedErr(fmt.Errorf("two identity directories for %s", id))
		}
		seen[id] = true
		mapped[entry.Name()] = id
	}
	return mapped, nil
}

// hashTree walks dir without following symlinks and returns each entry's
// relative path mapped to its SHA-256 ("dir" for a directory) and the total
// file size.
func hashTree(root *os.Root, dir string) (map[string]string, int64, error) {
	sums := map[string]string{}
	var size int64
	err := walkSafe(root, dir, func(rel string, info fs.FileInfo) error {
		if info.IsDir() {
			sums[rel] = "dir"
			return nil
		}
		data, err := root.ReadFile(path.Join(dir, rel))
		if err != nil {
			return persistenceErr(err)
		}
		sums[rel] = hash(data)
		size += int64(len(data))
		return nil
	})
	return sums, size, err
}

// copyTree copies dir into each destination, as 0700 directories and 0600
// files, syncing every file.
func copyTree(root *os.Root, dir string, destinations []string) error {
	return walkSafe(root, dir, func(rel string, info fs.FileInfo) error {
		var data []byte
		if !info.IsDir() {
			var err error
			if data, err = root.ReadFile(path.Join(dir, rel)); err != nil {
				return persistenceErr(err)
			}
		}
		for _, destination := range destinations {
			target := path.Join(destination, rel)
			var err error
			if info.IsDir() {
				err = mkdir(root, target)
			} else {
				err = writeFileSync(root, target, data)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// walkSafe visits dir and everything below it, rejecting symlinks, special
// files and anything not safely owned.
func walkSafe(root *os.Root, dir string, visit func(rel string, info fs.FileInfo) error) error {
	// WalkDir stats its start with symlinks followed, so check it first.
	if info, err := root.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		return unsupportedErr(err)
	} else if err != nil {
		return persistenceErr(err)
	} else if !info.IsDir() || checkOwned(info) != nil {
		return unsafeErr(dir)
	}
	return fs.WalkDir(root.FS(), dir, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return persistenceErr(err)
		}
		info, err := root.Lstat(p)
		if err != nil {
			return persistenceErr(err)
		}
		if !(info.IsDir() || info.Mode().IsRegular()) || checkOwned(info) != nil {
			return unsafeErr(p)
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(p, dir), "/")
		if rel == "" {
			rel = "."
		}
		return visit(rel, info)
	})
}

func syncTree(root *os.Root, dir string) error {
	var dirs []string
	if err := walkSafe(root, dir, func(rel string, info fs.FileInfo) error {
		if info.IsDir() {
			dirs = append(dirs, path.Join(dir, rel))
		}
		return nil
	}); err != nil {
		return err
	}
	for _, d := range append(dirs, path.Dir(dir)) {
		if err := syncDir(root, d); err != nil {
			return err
		}
	}
	return nil
}

func newGenerationID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func hash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func mkdir(root *os.Root, dir string) error {
	if err := root.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return persistenceErr(err)
	}
	if info, err := root.Lstat(dir); err != nil || !info.IsDir() || checkOwned(info) != nil {
		return unsafeErr(dir)
	}
	return nil
}

func readDir(root *os.Root, dir string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(root.FS(), dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

// readRegular reads a service-owned file, rejecting symlinks and unsafe owners.
func readRegular(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, unsupportedErr(err)
		}
		return nil, persistenceErr(err)
	}
	if !info.Mode().IsRegular() || checkOwned(info) != nil {
		return nil, unsafeErr(name)
	}
	data, err := root.ReadFile(name)
	if err != nil {
		return nil, persistenceErr(err)
	}
	return data, nil
}

func writeFileSync(root *os.Root, name string, data []byte) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return persistenceErr(err)
	}
	_, err = file.Write(data)
	err = errors.Join(err, file.Sync(), file.Close())
	if err != nil {
		return persistenceErr(err)
	}
	return nil
}

// replaceFile atomically replaces name: temp file, fsync, rename, fsync dir.
func replaceFile(root *os.Root, name string, data []byte) error {
	return replaceFileVia(root, name+".tmp", name, data, 0o600)
}

// replaceFileVia replaces name through temp, with mode: write, fsync,
// rename, then fsync both directories.
func replaceFileVia(root *os.Root, temp, name string, data []byte, mode fs.FileMode) error {
	if err := root.Remove(temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return persistenceErr(err)
	}
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return persistenceErr(err)
	}
	// The umask applies at creation, so set the exact mode before the fsync.
	err = file.Chmod(mode)
	if err == nil {
		_, err = file.Write(data)
	}
	if err := errors.Join(err, file.Sync(), file.Close()); err != nil {
		return persistenceErr(err)
	}
	if err := root.Rename(temp, name); err != nil {
		return persistenceErr(err)
	}
	if path.Dir(temp) != path.Dir(name) {
		if err := syncDir(root, path.Dir(temp)); err != nil {
			return err
		}
	}
	return syncDir(root, path.Dir(name))
}

func syncDir(root *os.Root, dir string) error {
	file, err := root.Open(dir)
	if err != nil {
		return persistenceErr(err)
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return persistenceErr(err)
	}
	return nil
}

func decodeStrict(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return errors.New("trailing data")
	}
	return nil
}
