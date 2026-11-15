package service

import (
	"fmt"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/chrisbanes/portico/helper/internal/portal"
)

var dnsLabelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Connection holds the runtime facts a snapshot may show. It is built only by
// sanitizeStatus, so runtime-supplied strings cannot reach clients unchecked.
type Connection struct {
	State              portal.State              `json:"state"`
	PublicAccessStatus portal.PublicAccessStatus `json:"publicAccessStatus,omitempty"`
	AssignedName       string                    `json:"assignedName,omitempty"`
	MagicDNSSuffix     string                    `json:"magicDNSSuffix,omitempty"`
	PortalURL          string                    `json:"portalURL,omitempty"`
	Addresses          []string                  `json:"addresses,omitempty"`
	// Freshness is "current", or "lastKnown" once the runtime that reported
	// it has gone.
	Freshness string `json:"freshness"`
}

const (
	freshnessCurrent   = "current"
	freshnessLastKnown = "lastKnown"
)

var knownStates = map[portal.State]bool{
	portal.StateAuthenticating: true, portal.StateAwaitingApproval: true, portal.StateConnecting: true,
	portal.StateOnline: true, portal.StateStopped: true, portal.StateError: true,
}

var knownPublicAccessStatuses = map[portal.PublicAccessStatus]bool{
	portal.PublicAccessStatusOff: true, portal.PublicAccessStatusPublishing: true,
	portal.PublicAccessStatusEnabled: true, portal.PublicAccessStatusBlocked: true,
}

// sanitizeStatus ports Swift's PortalStatusPayload.sanitizedForDisplay: it
// drops the stable node ID and tailnet name, and keeps only a DNS-label
// Assigned Name, a *.ts.net suffix, an https Portal URL for exactly that
// host, and literal IP addresses. Unknown states become error.
func sanitizeStatus(status portal.StatusEvent) Connection {
	c := Connection{State: status.State, Freshness: freshnessCurrent}
	if !knownStates[c.State] {
		c.State = portal.StateError
	}
	if knownPublicAccessStatuses[status.PublicAccessStatus] {
		c.PublicAccessStatus = status.PublicAccessStatus
	}
	if dnsLabelPattern.MatchString(status.AssignedName) {
		c.AssignedName = status.AssignedName
	}
	if isMagicDNSSuffix(status.MagicDNSSuffix) {
		c.MagicDNSSuffix = status.MagicDNSSuffix
	}
	if c.AssignedName != "" && c.MagicDNSSuffix != "" && isSafePortalURL(status.PortalURL, c.AssignedName+"."+c.MagicDNSSuffix) {
		c.PortalURL = status.PortalURL
	}
	for _, address := range status.Addresses {
		// Zones are free text, so a zoned address is not a safe fact.
		if parsed, err := netip.ParseAddr(address); err == nil && parsed.Zone() == "" && parsed.String() == address {
			c.Addresses = append(c.Addresses, address)
		}
	}
	return c
}

func isMagicDNSSuffix(suffix string) bool {
	if !strings.HasSuffix(suffix, ".ts.net") {
		return false
	}
	for _, label := range strings.Split(suffix, ".") {
		if !dnsLabelPattern.MatchString(label) {
			return false
		}
	}
	return true
}

func isSafePortalURL(raw, host string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && u.Opaque == "" &&
		(u.Path == "" || u.Path == "/") && strings.ToLower(u.Host) == host
}

// maxDiagnosticsSize bounds the diagnostics report.
const maxDiagnosticsSize = 64 << 10

// renderDiagnostics ports the Portal part of Swift's DiagnosticReportRenderer
// over sanitized facts only: no credentials, auth URLs, command lines or paths.
func renderDiagnostics(snapshot Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Portico service, management version %d\n", snapshot.Version)
	fmt.Fprintf(&b, "Runtime: %s\n", snapshot.Service.Runtime)
	fmt.Fprintf(&b, "Operational logging: %s\n\nPortals\n", snapshot.Logging)
	portals := slicesSortedByName(snapshot.Portals)
	for _, p := range portals {
		fmt.Fprintf(&b, "- Portal Name: %s\n", p.Name)
		fmt.Fprintf(&b, "  Lifecycle: %s\n  Desired: %s\n", p.Lifecycle, p.DesiredState)
		state, freshness := "unknown", freshnessCurrent
		if c := p.Connection; c != nil {
			state, freshness = string(c.State), c.Freshness
			line(&b, "  Assigned Name", c.AssignedName)
			line(&b, "  Portal URL", c.PortalURL)
			if len(c.Addresses) > 0 {
				fmt.Fprintf(&b, "  Tailscale IPs: %s\n", strings.Join(c.Addresses, ", "))
			}
			line(&b, "  MagicDNS suffix", c.MagicDNSSuffix)
		}
		fmt.Fprintf(&b, "  Tailscale: %s\n  Facts: %s\n", state, freshness)
		line(&b, "  Problem", string(p.Problem))
	}
	report := b.String()
	if len(report) > maxDiagnosticsSize {
		report = report[:maxDiagnosticsSize] + "\n[truncated]\n"
	}
	return report
}

func line(b *strings.Builder, label, value string) {
	if value != "" {
		fmt.Fprintf(b, "%s: %s\n", label, value)
	}
}

func slicesSortedByName(portals []PortalSnapshot) []PortalSnapshot {
	sorted := append([]PortalSnapshot(nil), portals...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	return sorted
}
