package management

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/portal"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// collect gathers a client's subscription snapshots until it closes.
func collect(c *Client) (snapshots func() []service.Snapshot) {
	var mu sync.Mutex
	var got []service.Snapshot
	go func() {
		for s := range c.Snapshots() {
			mu.Lock()
			got = append(got, s)
			mu.Unlock()
		}
	}()
	return func() []service.Snapshot {
		mu.Lock()
		defer mu.Unlock()
		return append([]service.Snapshot(nil), got...)
	}
}

func subscribe(t *testing.T, c *Client) func() []service.Snapshot {
	t.Helper()
	got := collect(c)
	for {
		// Concurrent mutations may hold every global call slot for a moment.
		err := c.Call(ctx(t), "subscribe", nil, nil, nil)
		var e *Error
		if errors.As(err, &e) && e.Code == "capacityExceeded" {
			time.Sleep(time.Millisecond)
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
}

func wantContiguous(t *testing.T, name string, snapshots []service.Snapshot, revision uint64) {
	t.Helper()
	if len(snapshots) == 0 {
		t.Fatalf("%s saw no snapshots", name)
	}
	for i := 1; i < len(snapshots); i++ {
		if snapshots[i].Sequence != snapshots[i-1].Sequence+1 || snapshots[i].Epoch != snapshots[0].Epoch {
			t.Fatalf("%s saw sequence %d after %d", name, snapshots[i].Sequence, snapshots[i-1].Sequence)
		}
	}
	if last := snapshots[len(snapshots)-1]; last.Revision != revision {
		t.Fatalf("%s ended at revision %d, want %d", name, last.Revision, revision)
	}
}

func TestSubscribersSeeEverySequenceInOrder(t *testing.T) {
	h := newHarness(t)
	setup := h.dial()
	chooseLogging(t, setup)
	id := createPortal(t, setup, "web")
	early := subscribe(t, h.dial())

	clients := make([]*Client, 12)
	for i := range clients {
		clients[i] = h.dial()
	}
	start := snapshot(t, setup).Revision
	const mutations = 100
	var wg sync.WaitGroup
	for i := range mutations {
		c := clients[i%len(clients)]
		wg.Go(func() {
			params := service.SetDestinationParams{PortalID: id, Destination: localApp(uint16(10000 + i))}
			for {
				var revision service.Snapshot
				err := c.Call(ctx(t), "getSnapshot", nil, nil, &revision)
				if err == nil {
					m := &Mutation{RequestID: newUUID(), IssuedAt: time.Now(), ExpectedRevision: revision.Revision}
					if err = c.Call(ctx(t), "setDestination", m, params, nil); err == nil {
						return
					}
				}
				var e *Error
				if !errors.As(err, &e) || (e.Code != "revisionConflict" && e.Code != "capacityExceeded") {
					t.Errorf("setDestination: %v", err)
					return
				}
			}
		})
	}
	// A subscriber joining mid-stream gets an initial snapshot that is
	// atomic with its registration: nothing missed or repeated after it.
	waitFor(t, "mutations under way", func() bool {
		s := early()
		return len(s) > 0 && s[len(s)-1].Revision >= start+mutations/4
	})
	late := subscribe(t, h.dial())
	wg.Wait()
	final := snapshot(t, setup).Revision
	if final != start+mutations {
		t.Fatalf("revision %d, want %d", final, start+mutations)
	}
	for name, got := range map[string]func() []service.Snapshot{"early": early, "late": late} {
		waitFor(t, name+" to see the final revision", func() bool {
			s := got()
			return len(s) > 0 && s[len(s)-1].Revision == final
		})
		wantContiguous(t, name, got(), final)
	}
}

// TestSlowConsumerIsDisconnectedWithoutStallingTheEngine floods a subscriber
// that never reads. The engine's callback only queues, so the flood and
// other clients' mutations stay prompt while that client is disconnected.
func TestSlowConsumerIsDisconnectedWithoutStallingTheEngine(t *testing.T) {
	h := newHarness(t)
	c := h.dial()
	chooseLogging(t, c)
	id := createPortal(t, c, "web")
	waitFor(t, "the Portal started", func() bool { return h.factory.runtime() != nil && h.factory.runtime().configured(id) })

	blocked := h.raw(t)
	rawHello(t, blocked)
	writeJSON(t, blocked, rawCall("subscribe", map[string]any{}))
	// Flood only once the subscription is registered.
	for {
		f, err := readRaw(t, blocked)
		if err != nil {
			t.Fatal(err)
		}
		if f["type"] == "snapshot" {
			break
		}
	}

	rt := h.factory.runtime()
	flooded := make(chan time.Duration)
	go func() {
		start := time.Now()
		states := []portal.State{portal.StateConnecting, portal.StateOnline}
		for i := range 1000 {
			rt.status(id, states[i%2])
		}
		flooded <- time.Since(start)
	}()
	committed := make(chan error)
	go func() {
		for i := range 20 {
			start := time.Now()
			if err := tryMutate(t, c, "setDestination", service.SetDestinationParams{PortalID: id, Destination: localApp(uint16(20000 + i))}); err != nil {
				committed <- err
				return
			}
			if elapsed := time.Since(start); elapsed > 2*time.Second {
				committed <- fmt.Errorf("a mutation took %v", elapsed)
				return
			}
		}
		committed <- nil
	}()
	select {
	case elapsed := <-flooded:
		if elapsed > 5*time.Second {
			t.Fatalf("1000 publications took %v", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the engine stalled behind a slow subscriber")
	}
	frames := readUntilClosed(t, blocked)
	select {
	case err := <-committed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("other clients' mutations stalled")
	}
	if last := frames[len(frames)-1]; last["type"] != "error" || errorCode(last) != "slowConsumer" {
		t.Fatalf("last frame = %v, want slowConsumer", last)
	}
	if len(frames) > 2*queueSize+2 {
		t.Fatalf("a slow client got %d frames", len(frames))
	}
}

// seedPortals is an installation of n Remote App Portals with long hosts.
func seedPortals(n int) string {
	host := strings.Repeat("a", 60) + "." + strings.Repeat("b", 60) + "." + strings.Repeat("c", 60) + ".example.com"
	portals := make([]string, n)
	for i := range portals {
		portals[i] = fmt.Sprintf(`{"id":"%s","name":"portal-%d","destination":{"kind":"remoteApp","scheme":"https","host":"%s","port":443},`+
			`"publicAccess":"private","createdAt":800000000,"desiredState":"stopped","lifecycle":"active"}`, newUUID(), i, host)
	}
	return `{"version":5,"portals":[` + strings.Join(portals, ",") + `],"alerts":[],"operationalLogging":"enabled","launchAtLoginOffer":"notOffered"}`
}

const largeInstall = 1300

func TestPagedSnapshotsArriveComplete(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.seed = seedPortals(largeInstall)
		h.limits.MaxFrame = 32 << 10
	})
	c := h.dial()
	var s service.Snapshot
	if err := c.Call(ctx(t), "getSnapshot", nil, nil, &s); err != nil {
		t.Fatal(err)
	}
	if encoded, _ := json.Marshal(s); len(s.Portals) != largeInstall || len(encoded) <= 1<<20 {
		t.Fatalf("snapshot has %d Portals in %d bytes, want %d in over 1 MiB", len(s.Portals), len(encoded), largeInstall)
	}
	direct := h.engine.Connect().GetSnapshot()
	for i := range direct.Portals {
		if s.Portals[i].ID != direct.Portals[i].ID || s.Portals[i].Destination != direct.Portals[i].Destination {
			t.Fatalf("Portal %d = %+v, want %+v", i, s.Portals[i], direct.Portals[i])
		}
	}
	got := subscribe(t, c)
	waitFor(t, "the paged initial snapshot", func() bool { return len(got()) == 1 })
	if first := got()[0]; len(first.Portals) != largeInstall || first.Revision != direct.Revision {
		t.Fatalf("initial snapshot has %d Portals at revision %d", len(first.Portals), first.Revision)
	}

	// On the wire: one ordered set within the frame limit.
	r := h.raw(t)
	rawHello(t, r)
	call := rawCall("getSnapshot", map[string]any{})
	writeJSON(t, r, call)
	var frames []frame
	for len(frames) == 0 || frames[len(frames)-1]["type"] != "snapshotEnd" {
		payload, err := ipc.ReadFrame(r, h.limits.MaxFrame)
		if err != nil {
			t.Fatalf("reading the paged set: %v", err)
		}
		var f frame
		if err := json.Unmarshal(payload, &f); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, f)
	}
	pages := len(frames) - 2
	if frames[0]["type"] != "snapshotBegin" || frames[0]["pageCount"] != float64(pages) || pages > maxPages || pages < 32 {
		t.Fatalf("begin = %v with %d pages", frames[0], pages)
	}
	for i, f := range frames {
		_, hasSnapshot := f["snapshot"]
		if f["callID"] != call["callID"] || (i > 0 && i <= pages && (f["type"] != "snapshotPage" || f["pageIndex"] != float64(i-1) || hasSnapshot != (i == 1))) {
			t.Fatalf("frame %d = %v", i, f["type"])
		}
	}
}

func TestOversizedSnapshotSetsAreRefusedWhole(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.seed = seedPortals(200)
		h.limits.MaxFrame = 2 << 10 // over 64 pages
	})
	r := h.raw(t)
	rawHello(t, r)
	call := rawCall("getSnapshot", map[string]any{})
	writeJSON(t, r, call)
	if reply := readJSON(t, r); reply["type"] != "reply" || reply["callID"] != call["callID"] || errorCode(reply) != "capacityExceeded" {
		t.Fatalf("first frame = %v, want only the capacityExceeded reply", reply)
	}

	s := h.raw(t)
	rawHello(t, s)
	writeJSON(t, s, rawCall("subscribe", map[string]any{}))
	frames := readUntilClosed(t, s)
	if len(frames) != 2 || frames[0]["type"] != "reply" || frames[1]["type"] != "error" || errorCode(frames[1]) != "capacityExceeded" {
		t.Fatalf("subscription frames = %v, want the reply then capacityExceeded", frames)
	}
}

// TestPagedSetsAreContiguous checks no frame lands inside another set while
// replies and subscription snapshots page concurrently on one connection.
func TestPagedSetsAreContiguous(t *testing.T) {
	h := newHarness(t, func(h *harness) {
		h.seed = seedPortals(12)
		h.limits.MaxFrame = 4 << 10
		// Contiguity, not backpressure: the reader may lag on one CPU.
		h.limits.OutputQueue = 4096
	})
	c := h.dial()
	id := snapshot(t, c).Portals[0].ID
	r := h.raw(t)
	rawHello(t, r)
	writeJSON(t, r, rawCall("subscribe", map[string]any{}))
	frames := make(chan frame, 4096)
	go func() {
		defer close(frames)
		for {
			payload, err := ipc.ReadFrame(r, ipc.MaxFrame)
			if err != nil {
				return
			}
			var f frame
			_ = json.Unmarshal(payload, &f)
			frames <- f
		}
	}()
	var mutator sync.WaitGroup
	defer mutator.Wait()
	mutator.Go(func() {
		for i := range 50 {
			_ = tryMutate(t, c, "setDesiredState", service.SetDesiredStateParams{PortalID: id, State: []portal.DesiredState{portal.DesiredStateEnabled, portal.DesiredStateStopped}[i%2]})
		}
	})
	var all []frame
	for range 20 {
		for range h.limits.CallsPerConn {
			writeJSON(t, r, rawCall("getSnapshot", map[string]any{}))
		}
		for ended := 0; ended < h.limits.CallsPerConn; {
			f, ok := <-frames
			if !ok {
				t.Fatal("the connection closed")
			}
			all = append(all, f)
			// A call ends with its set, or a refusal from a slot not yet released.
			if f["type"] == "snapshotEnd" && f["callID"] != nil || f["type"] == "reply" {
				ended++
			}
		}
	}
	var open frame
	for i, f := range all {
		switch {
		case open != nil && (f["type"] != "snapshotPage" && f["type"] != "snapshotEnd" || f["callID"] != open["callID"]):
			t.Fatalf("frame %d (%v) interrupts the set begun as %v", i, f["type"], open)
		case f["type"] == "snapshotBegin":
			open = f
		case f["type"] == "snapshotEnd":
			open = nil
		}
	}
}
