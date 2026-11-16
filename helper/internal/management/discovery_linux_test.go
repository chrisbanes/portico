//go:build linux

package management

import (
	"testing"

	"github.com/chrisbanes/portico/helper/internal/service"
)

// The initial Linux service has no automatic discovery; explicit ports work.
func TestLinuxDiscoveryIsUnavailableAndExplicitPortsWork(t *testing.T) {
	h := newHarness(t)
	c := h.dial()
	if c.Capabilities.AutomaticDiscovery != "unavailable" {
		t.Fatalf("capabilities = %+v", c.Capabilities)
	}
	wantCode(t, c.Call(ctx(t), "discoverLocalApps", nil, nil, nil), service.CodeCapabilityUnavailable, "")
	chooseLogging(t, c)
	if id := createPortal(t, c, "explicit"); findPortal(snapshot(t, c), id) == nil {
		t.Fatal("an explicit-port create did not persist")
	}
}
