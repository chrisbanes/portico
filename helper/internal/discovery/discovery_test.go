package discovery

import (
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
)

func TestDiscoverSuggestsConservativePortalNames(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		argv       []string
		wantLabel  string
		wantHint   string
	}{
		{"Java jar", "/usr/bin/java", []string{"java", "-jar", "/private/tools/Hermes Server.jar", "--token", "do-not-copy"}, "java", "hermes-server"},
		{"Python module", "/usr/bin/python3", []string{"python3", "-m", "package.Hermes_Server", "--secret", "do-not-copy"}, "python3", "hermes-server"},
		{"Python script", "/usr/bin/python3", []string{"python3", "/private/tools/Hermes.py", "--secret", "do-not-copy"}, "python3", "hermes"},
		{"Node entry", "/opt/homebrew/bin/node", []string{"node", "/private/tools/Hermes Server.mjs", "--secret", "do-not-copy"}, "node", "hermes-server"},
		{"Direct executable", "/Applications/Tools/Hermes_Server", []string{"Hermes_Server", "--secret", "do-not-copy"}, "Hermes_Server", "hermes-server"},
		{"Bare Java", "/usr/bin/java", []string{"java"}, "java", ""},
		{"Bare Python", "/usr/bin/python3", []string{"python3"}, "python3", ""},
		{"Bare Node", "/usr/bin/node", []string{"node"}, "node", ""},
		{"Shell", "/bin/zsh", []string{"zsh", "/private/tools/hermes"}, "zsh", ""},
		{"Launcher", "/usr/bin/env", []string{"env", "SECRET=do-not-copy", "/private/tools/hermes"}, "env", ""},
		{"Generic runtime", "/usr/bin/ruby", []string{"ruby", "/private/tools/hermes.rb"}, "ruby", ""},
		{"Java leading flag", "/usr/bin/java", []string{"java", "-Xmx1g", "-jar", "/private/tools/hermes.jar"}, "java", ""},
		{"Python leading flag", "/usr/bin/python3", []string{"python3", "-u", "/private/tools/hermes.py"}, "python3", ""},
		{"Node leading flag", "/usr/bin/node", []string{"node", "--inspect", "/private/tools/hermes.js"}, "node", ""},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := uint16(8000 + index)
			got := reduce(port, test.executable, test.argv)
			want := Candidate{LocalAppPort: port, ProcessLabel: test.wantLabel, SuggestedPortalName: test.wantHint}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Discover() = %+v, want %+v", got, want)
			}
			for _, forbidden := range []string{"--secret", "do-not-copy", "/private/tools"} {
				if strings.Contains(got.ProcessLabel, forbidden) || strings.Contains(got.SuggestedPortalName, forbidden) {
					t.Fatal("sensitive argument data escaped the discovery boundary")
				}
			}
		})
	}
}

func TestDiscoverRejectsInvalidSuggestionsAndSanitizesLabels(t *testing.T) {
	overlong := strings.Repeat("a", 64)
	discoverer := newDiscoverer(
		fakeSource{candidates: []Candidate{
			reduce(9001, "/tmp/../", []string{""}),
			reduce(9002, "/tmp/python", []string{"python", "-m", "package.!!!"}),
			reduce(9003, "/tmp/node", []string{"node", overlong + ".js"}),
			reduce(9004, "/tmp/<unsafe process>", []string{"unsafe"}),
		}},
		fakeProber{reachable: map[uint16]bool{9001: true, 9002: true, 9003: true, 9004: true}},
	)

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{
		{LocalAppPort: 9001, ProcessLabel: "Port 9001"},
		{LocalAppPort: 9002, ProcessLabel: "python"},
		{LocalAppPort: 9003, ProcessLabel: "node"},
		{LocalAppPort: 9004, ProcessLabel: "unsafe-process", SuggestedPortalName: "unsafe-process"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %+v, want %+v", got, want)
	}
}

func TestDiscoverDeduplicatesAndSortsByPort(t *testing.T) {
	discoverer := newDiscoverer(
		fakeSource{candidates: []Candidate{
			reduce(9000, "/tmp/hermes", []string{"hermes"}),
			reduce(8000, "/tmp/atlas", []string{"atlas"}),
			reduce(9000, "/other/hermes", []string{"hermes", "--secret", "do-not-copy"}),
			reduce(7000, "/tmp/first", []string{"first"}),
			reduce(8000, "/tmp/different", []string{"different"}),
		}},
		fakeProber{reachable: map[uint16]bool{7000: true, 8000: true, 9000: true}},
	)

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{
		{LocalAppPort: 7000, ProcessLabel: "first", SuggestedPortalName: "first"},
		{LocalAppPort: 8000, ProcessLabel: "Port 8000"},
		{LocalAppPort: 9000, ProcessLabel: "hermes", SuggestedPortalName: "hermes"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %+v, want %+v", got, want)
	}
}

func TestDiscoverIncludesOnlyAcceptingLoopbackPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	openPort := uint16(listener.Addr().(*net.TCPAddr).Port)

	closedListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := uint16(closedListener.Addr().(*net.TCPAddr).Port)
	closedListener.Close()

	discoverer := newDiscoverer(
		fakeSource{candidates: []Candidate{
			reduce(closedPort, "/tmp/closed", []string{"closed"}),
			reduce(openPort, "/tmp/accepting", []string{"accepting"}),
		}},
		loopbackProber{},
	)

	got, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Candidate{{LocalAppPort: openPort, ProcessLabel: "accepting", SuggestedPortalName: "accepting"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Discover() = %+v, want %+v", got, want)
	}
}

type fakeSource struct {
	candidates []Candidate
	err        error
}

func (s fakeSource) listeners(context.Context) ([]Candidate, error) {
	return s.candidates, s.err
}

type fakeProber struct {
	reachable map[uint16]bool
}

func (p fakeProber) reachableAtLoopback(_ context.Context, port uint16) bool {
	return p.reachable[port]
}
