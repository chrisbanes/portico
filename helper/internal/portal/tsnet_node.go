package portal

import (
	"context"
	"errors"
	"net"
	"os"

	"tailscale.com/client/local"
	"tailscale.com/ipn"
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
	mapped := Status{
		BackendState: status.BackendState,
		CertDomains:  append([]string(nil), status.CertDomains...),
		Addresses:    make([]string, 0, len(status.TailscaleIPs)),
	}
	for _, address := range status.TailscaleIPs {
		mapped.Addresses = append(mapped.Addresses, address.String())
	}
	if status.Self != nil {
		mapped.StableNodeID = string(status.Self.ID)
		mapped.DNSName = status.Self.DNSName
	}
	return mapped, nil
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

func (n *tsnetNode) ListenTLS(network, address string) (net.Listener, error) {
	return n.server.ListenTLS(network, address)
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
