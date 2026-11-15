package installation

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/portal"
)

// Fixtures in testdata are copied verbatim from the JSON in
// PorticoTests/PortalStoreTests.swift, except v5-public-local-app.json, which
// adds "publicAccess":"public" to the version 5 record in
// testInvalidVersionFivePublicAccessFailsClosedWithoutChangingAnyStoredBytes.

const (
	hermesID = "9f55ca93-d7b3-4eab-a871-310ea576005a"
	atlasID  = "5ea74329-3144-4ba2-925f-138d14d61fcc"
	alertID  = "1f93e456-69ec-445a-8374-d7fc5558d0c7"

	staleVersionTwo = `{"version":2,"portals":[],"alerts":[]}`
	staleVersionOne = `{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"stale","localAppPort":8787,"createdAt":807692800}}`
)

// Date(timeIntervalSince1970: 1_786_000_000) and 1_786_000_100 in the Swift tests.
var (
	portalCreatedAt = time.Unix(1_786_000_000, 0).UTC()
	alertCreatedAt  = time.Unix(1_786_000_100, 0).UTC()
)

func ptr(s string) *string { return &s }

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func localApp(port uint16) portal.Destination {
	return portal.Destination{Kind: portal.DestinationLocalApp, Port: port}
}

var oneTSNetBinding = &TailnetBinding{Name: "opaque-tailnet-id", MagicDNSSuffix: "one.ts.net"}

var rejectionAlert = Alert{
	ID:                     alertID,
	Kind:                   AlertCrossTailnetRejection,
	PortalName:             "hermes",
	AssignedName:           ptr("hermes-1"),
	ExpectedMagicDNSSuffix: ptr("one.ts.net"),
	RejectedMagicDNSSuffix: ptr("two.ts.net"),
	CreatedAt:              alertCreatedAt,
}

func hermes(destination portal.Destination, desired portal.DesiredState, lifecycle Lifecycle) Portal {
	return Portal{
		ID:           hermesID,
		Name:         "hermes",
		Destination:  destination,
		PublicAccess: portal.PublicAccessModePrivate,
		CreatedAt:    portalCreatedAt,
		DesiredState: desired,
		Lifecycle:    lifecycle,
	}
}

type fileState struct {
	data string
	mode fs.FileMode
}

// writeRoot creates a root holding files and returns it with a snapshot of
// every entry's bytes and mode.
func writeRoot(t *testing.T, files map[string]string) (string, map[string]fileState) {
	t.Helper()
	dir := t.TempDir()
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, snapshot(t, dir)
}

func snapshot(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]fileState{}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		var data string
		switch {
		case info.Mode().IsRegular():
			bytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			data = string(bytes)
		case info.Mode()&fs.ModeSymlink != 0:
			data, err = os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		state[entry.Name()] = fileState{data: data, mode: info.Mode()}
	}
	return state
}

func importDir(t *testing.T, dir string) (Installation, Source, error) {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	return Import(root)
}

func TestImportVersionsOneThroughFive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		source  string
		version int
		want    Installation
	}{
		{
			name:    "testVersionOneMigrationEnablesLoggingAndStartsLoginOfferUnspent",
			files:   map[string]string{"portal-v1.json": fixture(t, "v1.json")},
			source:  "portal-v1.json",
			version: 1,
			want: Installation{
				Portals:            []Portal{hermes(localApp(8787), portal.DesiredStateEnabled, LifecycleActive)},
				Logging:            LoggingEnabled,
				LaunchAtLoginOffer: OfferNotOffered,
			},
		},
		{
			name:    "testPristineVersionTwoMigrationRequiresLoggingChoice",
			files:   map[string]string{"installation-v2.json": fixture(t, "v2-pristine.json")},
			source:  "installation-v2.json",
			version: 2,
			want:    New(),
		},
		{
			name:    "testVersionTwoMigrationPreservesAllApplicableFactsAndAddsPrivateAccess",
			files:   map[string]string{"installation-v2.json": fixture(t, "v2.json"), "portal-v1.json": staleVersionOne},
			source:  "installation-v2.json",
			version: 2,
			want: Installation{
				Binding: oneTSNetBinding,
				Portals: []Portal{func() Portal {
					p := hermes(localApp(8787), portal.DesiredStateStopped, LifecyclePendingRemoval)
					p.RemovalAssignedName = ptr("hermes-1")
					return p
				}()},
				Alerts:             []Alert{rejectionAlert},
				Logging:            LoggingEnabled,
				LaunchAtLoginOffer: OfferNotOffered,
			},
		},
		{
			name:    "testHistoricalVersionTwoDefaultsToEnabledAndNextSavePersistsDesiredState",
			files:   map[string]string{"installation-v2.json": fixture(t, "v2-default-desired-state.json")},
			source:  "installation-v2.json",
			version: 2,
			want: Installation{
				Portals: []Portal{
					hermes(localApp(8787), portal.DesiredStateEnabled, LifecycleActive),
					{
						ID:           atlasID,
						Name:         "atlas",
						Destination:  localApp(8788),
						PublicAccess: portal.PublicAccessModePrivate,
						CreatedAt:    portalCreatedAt.Add(time.Second),
						DesiredState: portal.DesiredStateEnabled,
						Lifecycle:    LifecycleActive,
					},
				},
				Logging:            LoggingEnabled,
				LaunchAtLoginOffer: OfferNotOffered,
			},
		},
		{
			name:    "testVersionThreeMigrationWritesVersionFiveBeforeRemovingSource",
			files:   map[string]string{"installation-v3.json": fixture(t, "v3.json"), "installation-v2.json": staleVersionTwo},
			source:  "installation-v3.json",
			version: 3,
			want: Installation{
				Binding:            oneTSNetBinding,
				Portals:            []Portal{hermes(localApp(8787), portal.DesiredStateStopped, LifecyclePendingTailnetRejection)},
				Alerts:             []Alert{rejectionAlert},
				Logging:            LoggingDisabled,
				LaunchAtLoginOffer: OfferAccepted,
			},
		},
		{
			name: "testVersionFourRemoteAppMigrationPreservesInstallationFactsAndBecomesPrivate",
			files: map[string]string{
				"installation-v4.json": fixture(t, "v4-remote-app.json"),
				"installation-v3.json": `{"version":3,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`,
				"installation-v2.json": staleVersionTwo,
				"portal-v1.json":       staleVersionOne,
			},
			source:  "installation-v4.json",
			version: 4,
			want: Installation{
				Binding: oneTSNetBinding,
				Portals: []Portal{func() Portal {
					p := hermes(portal.Destination{Kind: portal.DestinationRemoteApp, Scheme: "https", Host: "app.example.com", Port: 8443}, portal.DesiredStateStopped, LifecyclePendingRemoval)
					p.RemovalAssignedName = ptr("hermes-1")
					return p
				}()},
				Alerts:             []Alert{rejectionAlert},
				Logging:            LoggingDisabled,
				LaunchAtLoginOffer: OfferAccepted,
			},
		},
		{
			name:    "testVersionFiveAcceptsPublicLocalApp",
			files:   map[string]string{"installation-v5.json": fixture(t, "v5-public-local-app.json"), "installation-v4.json": fixture(t, "v4-remote-app.json")},
			source:  "installation-v5.json",
			version: 5,
			want: Installation{
				Portals: []Portal{func() Portal {
					p := hermes(localApp(8787), portal.DesiredStateEnabled, LifecycleActive)
					p.PublicAccess = portal.PublicAccessModePublic
					return p
				}()},
				Logging:            LoggingEnabled,
				LaunchAtLoginOffer: OfferNotOffered,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, before := writeRoot(t, tc.files)
			got, source, err := importDir(t, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Import() = %+v, want %+v", got, tc.want)
			}
			if source.File != tc.source || source.Version != tc.version || string(source.Data) != tc.files[tc.source] {
				t.Fatalf("source = %s v%d, want %s v%d with the original bytes", source.File, source.Version, tc.source, tc.version)
			}
			if after := snapshot(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("Import changed the root: %v -> %v", before, after)
			}
		})
	}
}

func TestImportNonPristineVersionTwoKeepsLoggingEnabled(t *testing.T) {
	// testNonPristineVersionTwoMigrationsPreserveExistingLoggingBehavior
	for _, record := range []string{
		`{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"}],"alerts":[]}`,
		`{"version":2,"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"one.ts.net"},"portals":[],"alerts":[]}`,
		`{"version":2,"portals":[],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","createdAt":807692800}]}`,
	} {
		dir, _ := writeRoot(t, map[string]string{"installation-v2.json": record})
		got, _, err := importDir(t, dir)
		if err != nil {
			t.Fatal(record, err)
		}
		if got.Logging != LoggingEnabled || got.LaunchAtLoginOffer != OfferNotOffered {
			t.Fatalf("%s: logging %s, offer %s", record, got.Logging, got.LaunchAtLoginOffer)
		}
	}
}

func TestImportFreshRootReturnsNewInstallation(t *testing.T) {
	got, source, err := importDir(t, t.TempDir())
	if err != nil || !reflect.DeepEqual(got, New()) || source.File != "" {
		t.Fatalf("Import() = %+v, %+v, %v", got, source, err)
	}
}

const validVersionFour = `{"version":4,"portals":[],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`

func versionFive(portal string) string {
	return `{"version":5,"portals":[` + portal + `],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`
}

func versionFour(body string) string {
	return `{"version":4,` + body + `,"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`
}

func TestImportFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		file  string
		kind  ErrorKind
	}{
		{"corrupt v5 does not fall back", map[string]string{"installation-v5.json": "not-json", "installation-v2.json": staleVersionTwo}, "installation-v5.json", ErrCorrupt},
		// Sensitivity check from the plan: with v5 absent, a corrupt v4 must not fall back to v3.
		{"corrupt v4 does not fall back to v3", map[string]string{"installation-v4.json": "not-json", "installation-v3.json": fixture(t, "v3.json")}, "installation-v4.json", ErrCorrupt},
		{"corrupt v1", map[string]string{"portal-v1.json": "not-json"}, "portal-v1.json", ErrCorrupt},
		{"null top level", map[string]string{"installation-v5.json": "null"}, "installation-v5.json", ErrCorrupt},
		{"unsupported v5", map[string]string{"installation-v5.json": `{"version":6,"operationalLogging":"disabled","launchAtLoginOffer":"accepted","portals":[],"alerts":[]}`, "installation-v2.json": staleVersionTwo}, "installation-v5.json", ErrUnsupported},
		{"unknown version in v1 file", map[string]string{"portal-v1.json": `{"version":2,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":"2026-08-03T00:00:00Z"}}`}, "portal-v1.json", ErrUnsupported},
		{"explicit null desiredState", map[string]string{"installation-v2.json": `{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"desiredState":null,"lifecycle":"active"}],"alerts":[]}`}, "installation-v2.json", ErrCorrupt},
		{"v4 missing desiredState", map[string]string{"installation-v4.json": versionFour(`"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"lifecycle":"active"}],"alerts":[]`), "installation-v2.json": staleVersionTwo}, "installation-v4.json", ErrCorrupt},
		{"v3 port 0", map[string]string{"installation-v3.json": `{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":0,"createdAt":807692800,"lifecycle":"active"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`}, "installation-v3.json", ErrInvalid},
		{"v1 port 0", map[string]string{"portal-v1.json": `{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":0,"createdAt":807692800}}`}, "portal-v1.json", ErrInvalid},
		{"v4 port 0", map[string]string{"installation-v4.json": versionFour(`"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":0},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}],"alerts":[]`)}, "installation-v4.json", ErrCorrupt},
		{"v4 removalAssignedName on active", map[string]string{"installation-v4.json": versionFour(`"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active","removalAssignedName":"hermes-1"}],"alerts":[]`)}, "installation-v4.json", ErrCorrupt},
		{"v5 null removalAssignedName on active", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":"private","createdAt":807692800,"desiredState":"enabled","lifecycle":"active","removalAssignedName":null}`)}, "installation-v5.json", ErrCorrupt},
		{"v5 missing publicAccess", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}`), "installation-v4.json": validVersionFour, "portal-v1.json": staleVersionOne}, "installation-v5.json", ErrCorrupt},
		{"v5 null publicAccess", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":null,"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}`)}, "installation-v5.json", ErrCorrupt},
		{"v5 boolean publicAccess", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":true,"createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}`)}, "installation-v5.json", ErrCorrupt},
		{"v5 unknown publicAccess", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":"funnel","createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}`)}, "installation-v5.json", ErrInvalid},
		{"public Remote App", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"remoteApp","scheme":"https","host":"app.example.com","port":8443},"publicAccess":"public","createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}`)}, "installation-v5.json", ErrInvalid},
		{"destination with extra key", map[string]string{"installation-v5.json": versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787,"host":"x"},"publicAccess":"private","createdAt":807692800,"desiredState":"enabled","lifecycle":"active"}`)}, "installation-v5.json", ErrCorrupt},
		{"pendingTailnetRejection without binding", map[string]string{"installation-v3.json": `{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"pendingTailnetRejection"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`}, "installation-v3.json", ErrInvalid},
		{"duplicate portal IDs", map[string]string{"installation-v2.json": `{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"},{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"atlas","localAppPort":8788,"createdAt":807692801,"lifecycle":"active"}],"alerts":[]}`}, "installation-v2.json", ErrInvalid},
		{"duplicate portal IDs differing in case", map[string]string{"installation-v2.json": `{"version":2,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active"},{"id":"9f55ca93-d7b3-4eab-a871-310ea576005a","name":"atlas","localAppPort":8788,"createdAt":807692801,"lifecycle":"active"}],"alerts":[]}`}, "installation-v2.json", ErrInvalid},
		{"invalid name", map[string]string{"portal-v1.json": `{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"Invalid Name","localAppPort":8787,"createdAt":807692800}}`}, "portal-v1.json", ErrInvalid},
		{"invalid MagicDNS suffix", map[string]string{"installation-v4.json": versionFour(`"tailnetBinding":{"name":"opaque-tailnet-id","magicDNSSuffix":"not a suffix"},"portals":[],"alerts":[]`)}, "installation-v4.json", ErrInvalid},
		{"empty binding name", map[string]string{"installation-v4.json": versionFour(`"tailnetBinding":{"name":"","magicDNSSuffix":"example.ts.net"},"portals":[],"alerts":[]`)}, "installation-v4.json", ErrInvalid},
		{"invalid alert assigned name", map[string]string{"installation-v4.json": versionFour(`"portals":[],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","assignedName":"not a name","createdAt":807692800}]`)}, "installation-v4.json", ErrInvalid},
		{"duplicate alert IDs", map[string]string{"installation-v4.json": versionFour(`"portals":[],"alerts":[{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"hermes","createdAt":807692800},{"id":"1F93E456-69EC-445A-8374-D7FC5558D0C7","kind":"crossTailnetRejection","portalName":"atlas","createdAt":807692801}]`)}, "installation-v4.json", ErrInvalid},
		{"unknown lifecycle", map[string]string{"installation-v3.json": `{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"gone"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`}, "installation-v3.json", ErrInvalid},
		{"unknown logging", map[string]string{"installation-v3.json": `{"version":3,"portals":[],"alerts":[],"operationalLogging":"maybe","launchAtLoginOffer":"notOffered"}`}, "installation-v3.json", ErrInvalid},
		{"string createdAt", map[string]string{"portal-v1.json": `{"version":1,"portal":{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":"2026-08-03T00:00:00Z"}}`}, "portal-v1.json", ErrCorrupt},
		{"invalid UUID", map[string]string{"portal-v1.json": `{"version":1,"portal":{"id":"not-a-uuid","name":"hermes","localAppPort":8787,"createdAt":807692800}}`}, "portal-v1.json", ErrCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, before := writeRoot(t, tc.files)
			assertImportError(t, dir, tc.file, tc.kind)
			if after := snapshot(t, dir); !reflect.DeepEqual(after, before) {
				t.Fatalf("Import changed the root: %v -> %v", before, after)
			}
		})
	}
}

func assertImportError(t *testing.T, dir, file string, kind ErrorKind) {
	t.Helper()
	got, source, err := importDir(t, dir)
	var importErr *Error
	if !errors.As(err, &importErr) || importErr.File != file || importErr.Kind != kind {
		t.Fatalf("Import() error = %v, want %s %s", err, file, kind)
	}
	if !reflect.DeepEqual(got, Installation{}) || source.File != "" {
		t.Fatalf("Import() returned data with its error: %+v %+v", got, source)
	}
}

func TestImportRejectsUnsafeSources(t *testing.T) {
	t.Run("symlinked source", func(t *testing.T) {
		dir, _ := writeRoot(t, map[string]string{"real.json": fixture(t, "v5-public-local-app.json"), "installation-v4.json": validVersionFour})
		if err := os.Symlink("real.json", filepath.Join(dir, "installation-v5.json")); err != nil {
			t.Fatal(err)
		}
		before := snapshot(t, dir)
		assertImportError(t, dir, "installation-v5.json", ErrUnsafe)
		if after := snapshot(t, dir); !reflect.DeepEqual(after, before) {
			t.Fatalf("Import changed the root: %v -> %v", before, after)
		}
	})
	t.Run("directory in place of a source", func(t *testing.T) {
		dir, _ := writeRoot(t, map[string]string{"installation-v3.json": fixture(t, "v3.json")})
		if err := os.Mkdir(filepath.Join(dir, "installation-v4.json"), 0o700); err != nil {
			t.Fatal(err)
		}
		assertImportError(t, dir, "installation-v4.json", ErrUnsafe)
	})
	t.Run("stat error other than not found", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root bypasses directory permissions")
		}
		dir, before := writeRoot(t, map[string]string{"installation-v2.json": staleVersionTwo})
		root, err := os.OpenRoot(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close()
		// Without search permission every lookup fails with EACCES, not ENOENT.
		if err := os.Chmod(dir, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, importErr := Import(root)
		if err := os.Chmod(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		var e *Error
		if !errors.As(importErr, &e) || e.File != "installation-v5.json" || e.Kind != ErrUnreadable || errors.Is(importErr, fs.ErrNotExist) {
			t.Fatalf("Import() error = %v, want installation-v5.json unreadable", importErr)
		}
		if after := snapshot(t, dir); !reflect.DeepEqual(after, before) {
			t.Fatalf("Import changed the root: %v -> %v", before, after)
		}
	})
}

func TestImportKeepsSwiftDecodingEdgeCases(t *testing.T) {
	const fractional = 807692800.123456
	for _, tc := range []struct {
		name, file, record string
		check              func(Installation) bool
	}{
		{
			// Swift's createdAt carries sub-second precision from Date().
			"fractional createdAt", "installation-v5.json",
			versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":"private","createdAt":807692800.123456,"desiredState":"enabled","lifecycle":"active"}`),
			func(in Installation) bool {
				want := swiftReferenceDate.Add(time.Duration(fractional * float64(time.Second)))
				diff := in.Portals[0].CreatedAt.Sub(want)
				return diff < time.Microsecond && diff > -time.Microsecond && in.Portals[0].CreatedAt.Nanosecond() != 0
			},
		},
		{
			"v3 ignores removalAssignedName on an active Portal", "installation-v3.json",
			`{"version":3,"portals":[{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","localAppPort":8787,"createdAt":807692800,"lifecycle":"active","removalAssignedName":"x"}],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`,
			func(in Installation) bool { return in.Portals[0].RemovalAssignedName == nil },
		},
		{
			"v5 null removalAssignedName on a removing Portal", "installation-v5.json",
			versionFive(`{"id":"9F55CA93-D7B3-4EAB-A871-310EA576005A","name":"hermes","destination":{"kind":"localApp","port":8787},"publicAccess":"private","createdAt":807692800,"desiredState":"stopped","lifecycle":"pendingRemoval","removalAssignedName":null}`),
			func(in Installation) bool {
				return in.Portals[0].Lifecycle == LifecyclePendingRemoval && in.Portals[0].RemovalAssignedName == nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, _ := writeRoot(t, map[string]string{tc.file: tc.record})
			got, _, err := importDir(t, dir)
			if err != nil || len(got.Portals) != 1 || !tc.check(got) {
				t.Fatalf("Import() = %+v, %v", got, err)
			}
		})
	}
}
