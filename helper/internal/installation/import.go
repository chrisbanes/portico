package installation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"
	"time"

	"github.com/chrisbanes/portico/helper/internal/portal"
)

// files lists the historical records in precedence order. The first one that
// exists is authoritative; older files are never consulted after it.
var files = [...]struct {
	Name    string
	Version int
}{
	{"installation-v5.json", 5},
	{"installation-v4.json", 4},
	{"installation-v3.json", 3},
	{"installation-v2.json", 2},
	{"portal-v1.json", 1},
}

// maxSourceSize bounds how much of a source file Import reads.
const maxSourceSize = 64 << 20

type ErrorKind string

const (
	ErrUnreadable  ErrorKind = "unreadable"
	ErrUnsafe      ErrorKind = "unsafe"
	ErrUnsupported ErrorKind = "unsupported"
	// ErrCorrupt (undecodable) and ErrInvalid (decoded but breaks a rule) are
	// diagnostic only; both mean the record cannot be used.
	ErrCorrupt ErrorKind = "corrupt"
	ErrInvalid ErrorKind = "invalid"
)

// Error reports why Import refused a source. It never falls back to an older file.
type Error struct {
	File string
	Kind ErrorKind
	Err  error
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s: %v", e.File, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// Source identifies the record Import read. File is empty for a fresh root.
type Source struct {
	File    string
	Version int
	Data    []byte
}

// Import reads the authoritative historical record under root without
// writing anything. A root with no record yields New() and an empty Source.
func Import(root *os.Root) (Installation, Source, error) {
	for _, f := range files {
		data, err := readSource(root, f.Name)
		if err == errAbsent {
			continue
		}
		if err != nil {
			return Installation{}, Source{}, err
		}
		in, err := decode(data, f.Version)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				e = &Error{Kind: ErrCorrupt, Err: err}
			}
			e.File = f.Name
			return Installation{}, Source{}, e
		}
		if err := Validate(in); err != nil {
			return Installation{}, Source{}, &Error{File: f.Name, Kind: ErrInvalid, Err: err}
		}
		return in, Source{File: f.Name, Version: f.Version, Data: data}, nil
	}
	return New(), Source{}, nil
}

// errAbsent is returned unwrapped so that a file vanishing after Lstat stays
// an unreadable-source error rather than a fall-back to an older file.
var errAbsent = errors.New("absent")

func readSource(root *os.Root, name string) ([]byte, error) {
	info, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errAbsent
	}
	if err != nil {
		return nil, &Error{File: name, Kind: ErrUnreadable, Err: err}
	}
	if !info.Mode().IsRegular() {
		return nil, &Error{File: name, Kind: ErrUnsafe, Err: errors.New("not a regular file")}
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, &Error{File: name, Kind: ErrUnreadable, Err: err}
	}
	defer file.Close()
	// Reject a file swapped in (for example, for a symlink) after Lstat.
	if opened, err := file.Stat(); err != nil || !os.SameFile(info, opened) {
		return nil, &Error{File: name, Kind: ErrUnsafe, Err: errors.New("file changed while opening")}
	}
	data, err := io.ReadAll(io.LimitReader(file, maxSourceSize+1))
	if err != nil {
		return nil, &Error{File: name, Kind: ErrUnreadable, Err: err}
	}
	if len(data) > maxSourceSize {
		return nil, &Error{File: name, Kind: ErrCorrupt, Err: errors.New("file too large")}
	}
	return data, nil
}

func decode(data []byte, version int) (Installation, error) {
	top, err := decodeObject(data)
	if err != nil {
		return Installation{}, err
	}
	var stored int
	if err := top.required("version", &stored); err != nil {
		return Installation{}, err
	}
	if stored != version {
		return Installation{}, &Error{Kind: ErrUnsupported, Err: fmt.Errorf("version %d", stored)}
	}
	if version == 1 {
		return decodeVersionOne(top)
	}

	in := Installation{Logging: LoggingEnabled, LaunchAtLoginOffer: OfferNotOffered}
	var binding TailnetBinding
	if ok, err := top.optionalObject("tailnetBinding", func(o object) error {
		return errors.Join(o.required("name", &binding.Name), o.required("magicDNSSuffix", &binding.MagicDNSSuffix))
	}); err != nil {
		return Installation{}, err
	} else if ok {
		in.Binding = &binding
	}
	if err := top.requiredArray("portals", func(o object) error {
		p, err := decodePortal(o, version)
		in.Portals = append(in.Portals, p)
		return err
	}); err != nil {
		return Installation{}, err
	}
	if err := top.requiredArray("alerts", func(o object) error {
		a, err := decodeAlert(o)
		in.Alerts = append(in.Alerts, a)
		return err
	}); err != nil {
		return Installation{}, err
	}
	if version >= 3 {
		if err := errors.Join(
			top.required("operationalLogging", &in.Logging),
			top.required("launchAtLoginOffer", &in.LaunchAtLoginOffer),
		); err != nil {
			return Installation{}, err
		}
	} else if in.Binding == nil && len(in.Portals) == 0 && len(in.Alerts) == 0 {
		// A pristine version 2 install never recorded a logging choice.
		in.Logging = LoggingUndecided
	}
	return in, nil
}

func decodeVersionOne(top object) (Installation, error) {
	var p Portal
	if ok, err := top.optionalObject("portal", func(o object) error {
		var err error
		p, err = decodeHistoricalPortal(o, 1)
		return err
	}); err != nil {
		return Installation{}, err
	} else if !ok {
		return Installation{}, errors.New("missing portal")
	}
	return Installation{Portals: []Portal{p}, Logging: LoggingEnabled, LaunchAtLoginOffer: OfferNotOffered}, nil
}

func decodePortal(o object, version int) (Portal, error) {
	if version <= 3 {
		return decodeHistoricalPortal(o, version)
	}
	p := Portal{PublicAccess: portal.PublicAccessModePrivate}
	if err := errors.Join(
		o.uuid("id", &p.ID),
		o.required("name", &p.Name),
		o.required("destination", &p.Destination),
		o.date("createdAt", &p.CreatedAt),
		o.required("desiredState", &p.DesiredState),
		o.required("lifecycle", &p.Lifecycle),
	); err != nil {
		return Portal{}, err
	}
	if version == 5 {
		if err := o.required("publicAccess", &p.PublicAccess); err != nil {
			return Portal{}, err
		}
	}
	if p.Lifecycle != LifecyclePendingRemoval {
		if o.has("removalAssignedName") {
			return Portal{}, errors.New("only removing Portals may retain an assigned name")
		}
		return p, nil
	}
	return p, o.optionalString("removalAssignedName", &p.RemovalAssignedName)
}

// decodeHistoricalPortal reads the version 1–3 shape, which stored only a
// Local App port and defaulted a missing desired state to enabled.
func decodeHistoricalPortal(o object, version int) (Portal, error) {
	p := Portal{
		PublicAccess: portal.PublicAccessModePrivate,
		DesiredState: portal.DesiredStateEnabled,
		Lifecycle:    LifecycleActive,
	}
	var port uint16
	if err := errors.Join(
		o.uuid("id", &p.ID),
		o.required("name", &p.Name),
		o.required("localAppPort", &port),
		o.date("createdAt", &p.CreatedAt),
	); err != nil {
		return Portal{}, err
	}
	if port == 0 {
		return Portal{}, &Error{Kind: ErrInvalid, Err: errors.New("invalid historical destination")}
	}
	p.Destination = portal.Destination{Kind: portal.DestinationLocalApp, Port: port}
	if version == 1 {
		return p, nil
	}
	if o.has("desiredState") {
		if err := o.required("desiredState", &p.DesiredState); err != nil {
			return Portal{}, err
		}
	}
	if err := o.required("lifecycle", &p.Lifecycle); err != nil {
		return Portal{}, err
	}
	if p.Lifecycle != LifecyclePendingRemoval {
		return p, nil
	}
	return p, o.optionalString("removalAssignedName", &p.RemovalAssignedName)
}

func decodeAlert(o object) (Alert, error) {
	var a Alert
	return a, errors.Join(
		o.uuid("id", &a.ID),
		o.required("kind", &a.Kind),
		o.required("portalName", &a.PortalName),
		o.optionalString("assignedName", &a.AssignedName),
		o.optionalString("expectedMagicDNSSuffix", &a.ExpectedMagicDNSSuffix),
		o.optionalString("rejectedMagicDNSSuffix", &a.RejectedMagicDNSSuffix),
		o.date("createdAt", &a.CreatedAt),
	)
}

// object decodes JSON with Swift JSONDecoder's key rules: names match exactly,
// unknown keys are ignored, and a required key may not be null.
type object map[string]json.RawMessage

func decodeObject(data []byte) (object, error) {
	var o object
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, err
	}
	if o == nil {
		return nil, errors.New("expected an object")
	}
	return o, nil
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func (o object) has(key string) bool {
	_, ok := o[key]
	return ok
}

func (o object) required(key string, dst any) error {
	raw, ok := o[key]
	if !ok || isNull(raw) {
		return fmt.Errorf("missing %s", key)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

// optionalString treats an absent or null key as nil, like decodeIfPresent.
func (o object) optionalString(key string, dst **string) error {
	raw, ok := o[key]
	if !ok || isNull(raw) {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	*dst = &value
	return nil
}

func (o object) optionalObject(key string, decode func(object) error) (bool, error) {
	raw, ok := o[key]
	if !ok || isNull(raw) {
		return false, nil
	}
	child, err := decodeObject(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return true, decode(child)
}

func (o object) requiredArray(key string, decode func(object) error) error {
	var items []json.RawMessage
	if err := o.required(key, &items); err != nil {
		return err
	}
	for _, item := range items {
		child, err := decodeObject(item)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if err := decode(child); err != nil {
			return err
		}
	}
	return nil
}

// uuid reads a UUID string; Swift encoded uppercase, Portico stores lowercase.
func (o object) uuid(key string, dst *string) error {
	var value string
	if err := o.required(key, &value); err != nil {
		return err
	}
	*dst = strings.ToLower(value)
	if !uuidPattern.MatchString(*dst) {
		return fmt.Errorf("%s: invalid UUID", key)
	}
	return nil
}

// swiftReferenceDate is Foundation's 2001-01-01T00:00:00Z epoch.
var swiftReferenceDate = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// date reads a Swift JSONEncoder default Date: seconds since 2001-01-01.
func (o object) date(key string, dst *time.Time) error {
	var seconds float64
	if err := o.required(key, &seconds); err != nil {
		return err
	}
	// About ±31,700 years, which keeps the int64 conversion exact.
	if math.Abs(seconds) > 1e12 {
		return fmt.Errorf("%s: out of range", key)
	}
	whole := math.Floor(seconds)
	*dst = time.Unix(swiftReferenceDate.Unix()+int64(whole), int64(math.Round((seconds-whole)*1e9))).UTC()
	return nil
}
