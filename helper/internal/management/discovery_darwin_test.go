//go:build darwin && cgo

package management

import (
	"net"
	"testing"

	"github.com/chrisbanes/portico/helper/internal/discovery"
)

func TestDarwinDiscoveryFindsALoopbackListener(t *testing.T) {
	h := newHarness(t, func(h *harness) { h.opts.Discoverer = discovery.New() })
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := uint16(listener.Addr().(*net.TCPAddr).Port)

	c := h.dial()
	if c.Capabilities.AutomaticDiscovery != "available" {
		t.Fatalf("capabilities = %+v", c.Capabilities)
	}
	var found DiscoverResult
	if err := c.Call(ctx(t), "discoverLocalApps", nil, nil, &found); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range found.Candidates {
		if candidate.LocalAppPort == port {
			return
		}
	}
	t.Fatalf("port %d not among %+v", port, found.Candidates)
}
