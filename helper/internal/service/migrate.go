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
	"regexp"
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

	manifestSchema = 1
	phaseCommitted = "committed"
	stateFileName  = "state.json"

	// freeSpaceMargin is headroom beyond the copies for the state document
	// and manifest.
	freeSpaceMargin = 1 << 20
)

var generationIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Phase names a migration boundary passed to Options.Hook.
type Phase string

const (
	PhaseCopy           Phase = "copy"
	PhaseWrite          Phase = "write"
	PhaseFsync          Phase = "fsync"
	PhaseBeforeManifest Phase = "beforeManifest"
	PhaseAfterManifest  Phase = "afterManifest"
	PhaseCommitWrite    Phase = "commitWrite"
	PhaseCommitRename   Phase = "commitRename"
	PhaseCommitFsync    Phase = "commitFsync"
)

type Options struct {
	// Hook, when set, runs before each phase; an error aborts there as if
	// that filesystem operation had failed. Tests use it to inject faults.
	Hook func(Phase) error
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

type manifest struct {
	Schema     int               `json:"schema"`
	Generation string            `json:"generation"`
	Phase      string            `json:"phase"`
	Sources    map[string]string `json:"sources"`
}

// Open takes exclusive ownership of the state root and returns its store. A
// committed generation is loaded as is; otherwise the historical installation
// is imported into a new generation, which commits only when its manifest does.
//
// MIG-1, excluding older app and helper writers, is not implemented here; it
// is owned by #104/#105, so callers must pass an isolated root until then.
func Open(rootPath string, opts Options) (*Store, error) {
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
	s := &Store{root: root, lock: lock, opts: opts}
	if err := s.open(); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) open() error {
	// os.Root follows symlinks that stay inside the root, so a service
	// directory linked into the originals would redirect every write there.
	for _, dir := range []string{serviceDir, generationsDir, checkpointsDir, stagingDir} {
		if info, err := s.root.Lstat(dir); err == nil && (!info.IsDir() || checkOwned(info) != nil) {
			return unsafeErr(dir)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return persistenceErr(err)
		}
	}
	if _, err := s.root.Lstat(manifestFile); err == nil {
		return s.load()
	} else if !errors.Is(err, fs.ErrNotExist) {
		return persistenceErr(err)
	}
	// The manifest is the commit point, and generations appear only after it,
	// so committed data without a manifest is damage, not an interrupted import.
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
	return s.migrate()
}

func (s *Store) migrate() (err error) {
	imported, source, err := installation.Import(s.root)
	if err != nil {
		var importErr *installation.Error
		if errors.As(err, &importErr) && importErr.Kind == installation.ErrUnreadable {
			return persistenceErr(err)
		}
		return unsupportedErr(err)
	}
	if source.File != "" {
		if info, err := s.root.Lstat(source.File); err != nil || checkOwned(info) != nil {
			return unsafeErr(source.File)
		}
	}
	identities, err := mapIdentities(s.root, imported)
	if err != nil {
		return err
	}
	// Hash every original first: this also rejects unsafe entries before
	// anything is written, and sizes the free-space check.
	sources := map[string]string{}
	originals := map[string]map[string]string{}
	// The checkpoint keeps the source bytes and the state document is at most
	// their normalised size.
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
	if err := s.opts.hook(PhaseAfterManifest); err != nil {
		return err
	}
	return s.load()
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
	if info, err := root.Lstat(dir); err != nil {
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
	temp := name + ".tmp"
	if err := root.Remove(temp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return persistenceErr(err)
	}
	if err := writeFileSync(root, temp, data); err != nil {
		return err
	}
	if err := root.Rename(temp, name); err != nil {
		return persistenceErr(err)
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
