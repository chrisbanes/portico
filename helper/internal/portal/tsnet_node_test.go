package portal

import (
	"testing"

	"tailscale.com/ipn/ipnstate"
)

func TestTSNetStatusMapsNeedsLoginWithoutHTTPSReadiness(t *testing.T) {
	mapped := mapTSNetStatus(&ipnstate.Status{BackendState: "NeedsLogin"})
	if mapped.BackendState != "NeedsLogin" {
		t.Fatalf("mapped status = %+v, want NeedsLogin", mapped)
	}
	if err := validateHTTPSReadiness(mapped); err == nil {
		t.Fatal("HTTPS readiness accepted a node without MagicDNS or certificate state")
	}
}

func TestTSNetHTTPSReadinessAcceptsAssignedCertificateDomain(t *testing.T) {
	status := Status{
		MagicDNSSuffix: "example.ts.net",
		DNSName:        "hermes.example.ts.net.",
		CertDomains:    []string{"hermes.example.ts.net"},
	}
	if err := validateHTTPSReadiness(status); err != nil {
		t.Fatalf("validateHTTPSReadiness = %v", err)
	}
}

func TestTSNetHTTPSReadinessRejectsIncompleteCertificateState(t *testing.T) {
	for name, status := range map[string]Status{
		"missing MagicDNS suffix": {
			DNSName:     "hermes.example.ts.net.",
			CertDomains: []string{"hermes.example.ts.net"},
		},
		"missing assigned DNS name": {
			MagicDNSSuffix: "example.ts.net",
			CertDomains:    []string{"hermes.example.ts.net"},
		},
		"missing assigned certificate domain": {
			MagicDNSSuffix: "example.ts.net",
			DNSName:        "hermes.example.ts.net.",
			CertDomains:    []string{"other.example.ts.net"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateHTTPSReadiness(status); err == nil {
				t.Fatalf("validateHTTPSReadiness(%+v) = nil, want rejection", status)
			}
		})
	}
}
