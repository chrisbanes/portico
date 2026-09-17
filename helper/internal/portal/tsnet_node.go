package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"strings"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

type tsnetNode struct {
	server *tsnet.Server
	client *local.Client
}

func NewTSNetNode(dir, hostname string) Node {
	discard := func(string, ...any) {}
	return &tsnetNode{server: &tsnet.Server{
		Dir:       dir,
		Hostname:  hostname,
		Ephemeral: false,
		UserLogf:  discard,
		Logf:      discard,
	}}
}

func (n *tsnetNode) Start() error {
	if err := os.MkdirAll(n.server.Dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(n.server.Dir, 0o700); err != nil {
		return err
	}
	if err := n.server.Start(); err != nil {
		return err
	}
	client, err := n.server.LocalClient()
	if err != nil {
		return err
	}
	n.client = client
	return nil
}

func (n *tsnetNode) Status(ctx context.Context) (Status, error) {
	if n.client == nil {
		return Status{}, errors.New("tsnet node is not started")
	}
	status, err := n.client.StatusWithoutPeers(ctx)
	if err != nil {
		return Status{}, err
	}
	return mapTSNetStatus(status), nil
}

func (n *tsnetNode) Up(ctx context.Context) (Status, error) {
	if n.client == nil {
		return Status{}, errors.New("tsnet node is not started")
	}
	status, err := n.server.Up(ctx)
	if err != nil {
		return Status{}, err
	}
	mapped := mapTSNetStatus(status)
	if err := validateHTTPSReadiness(mapped); err != nil {
		return Status{}, err
	}
	return mapped, nil
}

func validateHTTPSReadiness(status Status) error {
	if status.MagicDNSSuffix == "" || status.DNSName == "" {
		return errors.New("tsnet node is not ready for HTTPS")
	}
	for _, domain := range status.CertDomains {
		if strings.EqualFold(strings.TrimSuffix(domain, "."), strings.TrimSuffix(status.DNSName, ".")) {
			return nil
		}
	}
	return errors.New("tsnet node has no HTTPS certificate domain")
}

func mapTSNetStatus(status *ipnstate.Status) Status {
	mapped := Status{
		BackendState: status.BackendState,
		CertDomains:  append([]string(nil), status.CertDomains...),
		Addresses:    make([]string, 0, len(status.TailscaleIPs)),
	}
	if status.CurrentTailnet != nil {
		mapped.TailnetName = status.CurrentTailnet.Name
		mapped.MagicDNSSuffix = status.CurrentTailnet.MagicDNSSuffix
	}
	for _, address := range status.TailscaleIPs {
		mapped.Addresses = append(mapped.Addresses, address.String())
	}
	if status.Self != nil {
		mapped.StableNodeID = string(status.Self.ID)
		mapped.DNSName = status.Self.DNSName
	}
	return mapped
}

func (n *tsnetNode) Watch(ctx context.Context) (Watcher, error) {
	if n.client == nil {
		return nil, errors.New("tsnet node is not started")
	}
	watcher, err := n.client.WatchIPNBus(ctx, ipn.NotifyInitialState)
	if err != nil {
		return nil, err
	}
	return &tsnetWatcher{watcher: watcher}, nil
}

func (n *tsnetNode) StartLoginInteractive(ctx context.Context) error {
	if n.client == nil {
		return errors.New("tsnet node is not started")
	}
	return n.client.StartLoginInteractive(ctx)
}

func (n *tsnetNode) Listen(network, address string) (net.Listener, error) {
	return n.server.Listen(network, address)
}

func (n *tsnetNode) TLSConfig() *tls.Config {
	if n.client == nil {
		return nil
	}
	return &tls.Config{GetCertificate: n.client.GetCertificate}
}

func (n *tsnetNode) Close() error { return n.server.Close() }

type tsnetWatcher struct {
	watcher *local.IPNBusWatcher
}

func (w *tsnetWatcher) Next() (Notification, error) {
	notification, err := w.watcher.Next()
	if err != nil {
		return Notification{}, err
	}
	var authURL string
	if notification.BrowseToURL != nil {
		authURL = *notification.BrowseToURL
	}
	return Notification{AuthURL: authURL}, nil
}

func (w *tsnetWatcher) Close() error { return w.watcher.Close() }
