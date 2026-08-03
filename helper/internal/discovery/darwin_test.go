//go:build darwin && cgo

package discovery

import (
	"context"
	"net"
	"reflect"
	"testing"
)

func TestDarwinDiscoveryObservesCurrentProcessListenerWithoutSensitiveFields(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)

	candidates, err := New().Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range candidates {
		if candidate.LocalAppPort == port {
			found = true
		}
	}
	if !found {
		t.Fatalf("current-process listener on port %d was not discovered", port)
	}

	typeOfCandidate := reflect.TypeOf(Candidate{})
	if typeOfCandidate.NumField() != 3 {
		t.Fatalf("Candidate has %d fields, want only sanitized port, label, and hint", typeOfCandidate.NumField())
	}
	for _, forbidden := range []string{"PID", "Path", "Argv", "CommandLine"} {
		if _, present := typeOfCandidate.FieldByName(forbidden); present {
			t.Fatalf("Candidate exposes sensitive field %q", forbidden)
		}
	}
}
