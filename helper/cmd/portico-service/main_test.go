package main

import (
	"bytes"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// build compiles portico-service with tags into a temporary directory.
func build(t *testing.T, tags string) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	bin := filepath.Join(t.TempDir(), "portico-service")
	out, err := exec.Command(gobin, "build", "-tags", tags, "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go build -tags %q: %v\n%s", tags, err, out)
	}
	return bin
}

// exitCode runs bin and returns its exit status and stderr. Every case
// fails before resolving a root, so nothing touches real state.
func exitCode(t *testing.T, bin string, args ...string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	cmd := exec.Command(bin, args...)
	cmd.Stderr = &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("%v: %v", args, err)
	}
	return exit.ExitCode(), stderr.String()
}

func TestProductionBuildRejectsFakeRuntime(t *testing.T) {
	bin := build(t, "")
	for _, flag := range []string{"fake-runtime", "pause-after-guard"} {
		code, stderr := exitCode(t, bin, "--variant", "test", "--"+flag, t.TempDir())
		if code != 2 || !strings.Contains(stderr, "flag provided but not defined: -"+flag) {
			t.Errorf("--%s: exit %d, stderr %q; want the flag unknown", flag, code, stderr)
		}
	}
}

// Production and Dev always have the guard on macOS, and no other OS has it.
func TestLegacyGuardFlagIsTestVariantOnMacOSOnly(t *testing.T) {
	bin := build(t, "")
	cases := [][]string{{"--variant", "production", "--legacy-guard"}, {"--variant", "dev", "--legacy-guard"}}
	if runtime.GOOS != "darwin" {
		cases = append(cases, []string{"--variant", "test", "--legacy-guard", "--state-root", t.TempDir(), "--runtime-dir", t.TempDir()})
	}
	for _, args := range cases {
		code, stderr := exitCode(t, bin, args...)
		if code != 2 || !strings.Contains(stderr, "--legacy-guard needs --variant test on macOS") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestFakeRuntimeIsTestVariantOnly(t *testing.T) {
	bin := build(t, "porticofake")
	for _, args := range [][]string{
		{"--variant", "production", "--fake-runtime", t.TempDir()},
		{"--variant", "dev", "--fake-runtime", t.TempDir()},
		{"--variant", "test", "--fake-runtime", "relative"},
	} {
		code, stderr := exitCode(t, bin, args...)
		if code != 2 || !strings.Contains(stderr, "--fake-runtime needs --variant test") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
	for _, args := range [][]string{
		{"--variant", "production", "--pause-after-guard", t.TempDir()},
		{"--variant", "test", "--pause-after-guard", "relative"},
	} {
		code, stderr := exitCode(t, bin, args...)
		if code != 2 || !strings.Contains(stderr, "--pause-after-guard needs --variant test") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}

func TestTestVariantRequiresBothRoots(t *testing.T) {
	bin := build(t, "")
	for _, args := range [][]string{
		{"--variant", "test"},
		{"--variant", "test", "--state-root", t.TempDir()},
		{"--variant", "test", "--runtime-dir", t.TempDir()},
		{"--variant", "test", "--state-root", "state", "--runtime-dir", t.TempDir()},
	} {
		if code, stderr := exitCode(t, bin, args...); code != 1 || !strings.Contains(stderr, "invalid state root") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
	if code, _ := exitCode(t, bin, "--variant", "staging"); code != 2 {
		t.Errorf("unknown variant: exit %d", code)
	}
}
