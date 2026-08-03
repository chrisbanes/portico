package discovery

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	probeTimeout      = 200 * time.Millisecond
	maxProbeWorkers   = 8
	maxProcessLabel   = 64
	maxPortalNameSize = 63
)

var errDiscoveryUnavailable = errors.New("local app discovery unavailable")

// Candidate is the complete privacy boundary for Local App discovery.
// Sensitive host-process details cannot be represented by this type.
type Candidate struct {
	LocalAppPort        uint16 `json:"localAppPort"`
	ProcessLabel        string `json:"processLabel"`
	SuggestedPortalName string `json:"suggestedPortalName,omitempty"`
}

type listenerSource interface {
	listeners(context.Context) ([]Candidate, error)
}

type reachabilityProber interface {
	reachableAtLoopback(context.Context, uint16) bool
}

type Discoverer struct {
	source listenerSource
	prober reachabilityProber
}

func newDiscoverer(source listenerSource, prober reachabilityProber) *Discoverer {
	return &Discoverer{source: source, prober: prober}
}

func (d *Discoverer) Discover(ctx context.Context) ([]Candidate, error) {
	candidates, err := d.source.listeners(ctx)
	if err != nil {
		return nil, errDiscoveryUnavailable
	}

	byPort := make(map[uint16]Candidate)
	disagreed := make(map[uint16]bool)
	for _, candidate := range candidates {
		if candidate.LocalAppPort == 0 {
			continue
		}
		if previous, exists := byPort[candidate.LocalAppPort]; !exists {
			byPort[candidate.LocalAppPort] = candidate
		} else if previous.ProcessLabel != candidate.ProcessLabel || previous.SuggestedPortalName != candidate.SuggestedPortalName {
			disagreed[candidate.LocalAppPort] = true
		}
	}
	for port := range disagreed {
		byPort[port] = genericCandidate(port)
	}

	ports := make([]uint16, 0, len(byPort))
	for port := range byPort {
		ports = append(ports, port)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	reachable := d.reachablePorts(ctx, ports)

	result := make([]Candidate, 0, len(reachable))
	for _, port := range ports {
		if reachable[port] {
			result = append(result, byPort[port])
		}
	}
	return result, nil
}

func (d *Discoverer) reachablePorts(ctx context.Context, ports []uint16) map[uint16]bool {
	result := make(map[uint16]bool, len(ports))
	if len(ports) == 0 {
		return result
	}

	type probeResult struct {
		port      uint16
		reachable bool
	}
	jobs := make(chan uint16)
	results := make(chan probeResult, len(ports))
	workers := min(maxProbeWorkers, len(ports))
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for port := range jobs {
				probeContext, cancel := context.WithTimeout(ctx, probeTimeout)
				reachable := d.prober.reachableAtLoopback(probeContext, port)
				cancel()
				results <- probeResult{port: port, reachable: reachable}
			}
		}()
	}
	go func() {
		for _, port := range ports {
			jobs <- port
		}
		close(jobs)
		group.Wait()
		close(results)
	}()
	for probe := range results {
		result[probe.port] = probe.reachable
	}
	return result
}

type loopbackProber struct{}

func (loopbackProber) reachableAtLoopback(ctx context.Context, port uint16) bool {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func reduce(port uint16, executable string, argv []string) Candidate {
	label := sanitizeProcessLabel(filepath.Base(executable))
	if label == "" {
		label = genericCandidate(port).ProcessLabel
	}
	return Candidate{
		LocalAppPort:        port,
		ProcessLabel:        label,
		SuggestedPortalName: classifyPortalName(filepath.Base(executable), argv),
	}
}

func genericCandidate(port uint16) Candidate {
	return Candidate{LocalAppPort: port, ProcessLabel: fmt.Sprintf("Port %d", port)}
}

func sanitizeProcessLabel(value string) string {
	if value == "." || value == ".." {
		return ""
	}
	var builder strings.Builder
	lastWasSeparator := false
	for _, char := range value {
		allowed := char <= unicode.MaxASCII && (unicode.IsLetter(char) || unicode.IsDigit(char) || strings.ContainsRune("._+-", char))
		if allowed {
			builder.WriteRune(char)
			lastWasSeparator = false
		} else if !lastWasSeparator {
			builder.WriteByte('-')
			lastWasSeparator = true
		}
		if builder.Len() > maxProcessLabel {
			return ""
		}
	}
	return strings.Trim(builder.String(), "-._+")
}

func classifyPortalName(executable string, argv []string) string {
	runtimeName := strings.ToLower(executable)
	if runtimeName == "" || isShellOrLauncher(runtimeName) {
		return ""
	}
	if runtimeName == "java" {
		if len(argv) >= 3 && argv[1] == "-jar" && !strings.HasPrefix(argv[2], "-") && strings.EqualFold(filepath.Ext(argv[2]), ".jar") {
			return normalizePortalName(filepath.Base(argv[2]), ".jar")
		}
		return ""
	}
	if isPython(runtimeName) {
		if len(argv) < 2 {
			return ""
		}
		if argv[1] == "-m" && len(argv) >= 3 && isPythonModule(argv[2]) {
			parts := strings.Split(argv[2], ".")
			return normalizePortalName(parts[len(parts)-1], "")
		}
		if strings.HasPrefix(argv[1], "-") {
			return ""
		}
		if strings.EqualFold(filepath.Ext(argv[1]), ".py") {
			return normalizePortalName(filepath.Base(argv[1]), ".py")
		}
		return ""
	}
	if runtimeName == "node" || runtimeName == "nodejs" {
		if len(argv) < 2 || argv[1] == "" || strings.HasPrefix(argv[1], "-") {
			return ""
		}
		return normalizePortalName(filepath.Base(argv[1]), recognizedNodeExtension(argv[1]))
	}
	return normalizePortalName(executable, "")
}

func isPython(value string) bool {
	if value == "python" || value == "python2" || value == "python3" {
		return true
	}
	for _, prefix := range []string{"python2.", "python3."} {
		if strings.HasPrefix(value, prefix) && onlyASCIIDigits(strings.TrimPrefix(value, prefix)) {
			return true
		}
	}
	return false
}

func isShellOrLauncher(value string) bool {
	switch value {
	case "sh", "bash", "dash", "zsh", "fish", "csh", "tcsh",
		"env", "xcrun", "nohup", "open", "launchctl",
		"ruby", "perl", "php", "swift", "go", "cargo", "dotnet", "mono",
		"npm", "npx", "yarn", "pnpm", "bun", "deno":
		return true
	default:
		return false
	}
}

func onlyASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isPythonModule(value string) bool {
	if value == "" || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") {
		return false
	}
	for _, component := range strings.Split(value, ".") {
		if component == "" {
			return false
		}
		for index, char := range component {
			if char > unicode.MaxASCII || !(char == '_' || unicode.IsLetter(char) || (index > 0 && unicode.IsDigit(char))) {
				return false
			}
		}
	}
	return true
}

func recognizedNodeExtension(value string) string {
	switch extension := strings.ToLower(filepath.Ext(value)); extension {
	case ".js", ".mjs", ".cjs", ".ts":
		return extension
	default:
		return ""
	}
}

func normalizePortalName(value, extension string) string {
	if extension != "" && strings.EqualFold(filepath.Ext(value), extension) {
		value = strings.TrimSuffix(value, filepath.Ext(value))
	}
	var builder strings.Builder
	lastWasSeparator := false
	for _, char := range value {
		if char >= 'A' && char <= 'Z' {
			char += 'a' - 'A'
		}
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			builder.WriteRune(char)
			lastWasSeparator = false
		} else if !lastWasSeparator {
			builder.WriteByte('-')
			lastWasSeparator = true
		}
		if builder.Len() > maxPortalNameSize {
			return ""
		}
	}
	result := strings.Trim(builder.String(), "-")
	if result == "" || len(result) > maxPortalNameSize {
		return ""
	}
	return result
}
