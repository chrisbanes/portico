package fakeruntime

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/portal"
)

func TestRuntimeLogsEveryCall(t *testing.T) {
	dir := t.TempDir()
	rt, err := Factory(dir)("", installation.LoggingEnabled)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := rt.Reconcile(t.Context(), []portal.Config{
		{ID: "a", DesiredState: portal.DesiredStateEnabled},
		{ID: "b", DesiredState: portal.DesiredStateStopped},
	}, nil)
	if err != nil || len(entries) != 2 || entries[1].Outcome != portal.OutcomeConverged {
		t.Fatalf("Reconcile() = %v, %v", entries, err)
	}
	if err := rt.RemovePortal(t.Context(), "c"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, FailRemovalName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rt.RemovePortal(t.Context(), "c"); err == nil {
		t.Fatal("RemovePortal() succeeded with the failure marker present")
	}
	if err := rt.Close(t.Context()); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(filepath.Join(dir, LogName))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var got []Entry
	for s := bufio.NewScanner(f); s.Scan(); {
		var e Entry
		if err := json.Unmarshal(s.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.PID != os.Getpid() {
			t.Fatalf("pid = %d", e.PID)
		}
		e.PID = 0
		got = append(got, e)
	}
	want := []Entry{
		{Call: "start"},
		{Call: "reconcile", Enabled: []string{"a"}, Stopped: []string{"b"}},
		{Call: "remove", PortalID: "c"},
		{Call: "remove", PortalID: "c", Failed: true},
		{Call: "close"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("log = %+v\nwant %+v", got, want)
	}
}
