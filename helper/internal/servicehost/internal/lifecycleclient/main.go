// lifecycleclient drives a test-variant portico-service over real
// management IPC for Scripts/test-service-lifecycle.sh. Usage:
//
//	lifecycleclient -runtime-dir DIR state
//	lifecycleclient -runtime-dir DIR mutate METHOD PARAMS-JSON REQUEST-FILE
//	lifecycleclient -runtime-dir DIR subscribe
//
// mutate saves its request identity in REQUEST-FILE, or replays the one
// already there, so a retry can cross a service restart.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/management"
	"github.com/chrisbanes/portico/helper/internal/service"
)

func main() {
	runtimeDir := flag.String("runtime-dir", "", "the test service's absolute runtime directory")
	flag.Parse()
	if err := run(*runtimeDir, flag.Args()); err != nil {
		var e *management.Error
		if errors.As(err, &e) {
			fmt.Println("error", e.Code)
			os.Exit(3)
		}
		fmt.Fprintln(os.Stderr, "lifecycleclient:", err)
		os.Exit(1)
	}
}

func run(runtimeDir string, args []string) error {
	endpoint, err := ipc.EndpointIn(runtimeDir, ipc.Test)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	c, err := management.Dial(ctx, endpoint)
	if err != nil {
		return err
	}
	defer c.Close()
	switch {
	case len(args) == 1 && args[0] == "state":
		var s service.Snapshot
		if err := c.Call(ctx, "getSnapshot", nil, nil, &s); err != nil {
			return err
		}
		fmt.Printf("revision=%d runtime=%s startupFailure=%s\n", s.Revision, s.Service.Runtime, s.Service.StartupFailure)
		for _, p := range s.Portals {
			fmt.Println("portal", p.ID, p.Name, p.DesiredState, p.Lifecycle, p.Problem)
		}
	case len(args) == 4 && args[0] == "mutate":
		m, err := request(ctx, c, args[3])
		if err != nil {
			return err
		}
		var r management.Receipt
		if err := c.Call(ctx, args[1], m, json.RawMessage(args[2]), &r); err != nil {
			return err
		}
		out, _ := json.Marshal(r)
		fmt.Println(string(out))
	case len(args) == 1 && args[0] == "subscribe":
		var s service.Snapshot
		if err := c.Call(ctx, "subscribe", nil, nil, &s); err != nil {
			return err
		}
		fmt.Println("subscribed")
		cancel()
		for range c.Snapshots() {
		}
	default:
		return errors.New("usage: state | mutate METHOD PARAMS-JSON REQUEST-FILE | subscribe")
	}
	return nil
}

// request loads the saved request identity, or makes and saves a new one at
// the current revision.
func request(ctx context.Context, c *management.Client, file string) (*management.Mutation, error) {
	var m management.Mutation
	if data, err := os.ReadFile(file); err == nil {
		return &m, json.Unmarshal(data, &m)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	var s service.Snapshot
	if err := c.Call(ctx, "getSnapshot", nil, nil, &s); err != nil {
		return nil, err
	}
	m = management.Mutation{RequestID: uuid(), IssuedAt: time.Now().UTC(), ExpectedRevision: s.Revision}
	data, _ := json.Marshal(m)
	return &m, os.WriteFile(file, data, 0o600)
}

func uuid() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
