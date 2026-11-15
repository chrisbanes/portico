package management

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chrisbanes/portico/helper/internal/installation"
	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

func smallSnapshot(portals int) service.Snapshot {
	s := service.Snapshot{Version: 1, Epoch: newUUID(), Sequence: 7, Revision: 3, Alerts: []installation.Alert{}, Portals: []service.PortalSnapshot{}}
	for i := range portals {
		s.Portals = append(s.Portals, service.PortalSnapshot{ID: newUUID(), Name: strings.Repeat("n", 40+i), Destination: localApp(uint16(1000 + i))})
	}
	return s
}

// pagedFrames pages s and decodes the frames as the client sees them.
func pagedFrames(t *testing.T, s service.Snapshot, maxFrame int) []clientFrame {
	t.Helper()
	payloads, err := snapshotFrames(s, "", maxFrame)
	if err != nil {
		t.Fatal(err)
	}
	frames := make([]clientFrame, len(payloads))
	for i, p := range payloads {
		if len(p) > maxFrame {
			t.Fatalf("frame %d is %d bytes, over %d", i, len(p), maxFrame)
		}
		if err := json.Unmarshal(p, &frames[i]); err != nil {
			t.Fatal(err)
		}
	}
	return frames
}

func feedAll(frames []clientFrame) []service.Snapshot {
	a := assembler{}
	var presented []service.Snapshot
	for _, f := range frames {
		if s, ok := a.feed(f); ok {
			presented = append(presented, s)
		}
	}
	return presented
}

func TestPagingRoundTripsThroughTheAssembler(t *testing.T) {
	s := smallSnapshot(40)
	frames := pagedFrames(t, s, 1024)
	if pages := len(frames) - 2; pages < 3 {
		t.Fatalf("only %d pages", pages)
	}
	presented := feedAll(frames)
	if len(presented) != 1 || !reflect.DeepEqual(presented[0], s) {
		t.Fatalf("presented %+v, want %+v", presented, s)
	}
}

func TestAssemblerPresentsOnlyCompleteOrderedSets(t *testing.T) {
	frames := pagedFrames(t, smallSnapshot(40), 1024)
	last := len(frames) - 1
	without := func(i int) []clientFrame {
		return append(append([]clientFrame{}, frames[:i]...), frames[i+1:]...)
	}
	swapped := append([]clientFrame{}, frames...)
	swapped[1], swapped[2] = swapped[2], swapped[1]
	moreAnnounced := append([]clientFrame{}, frames...)
	moreAnnounced[0].PageCount++
	wrongEnd := append([]clientFrame{}, frames...)
	wrongEnd[last].Sequence++
	headless := append([]clientFrame{}, frames...)
	headless[1].Snapshot = nil
	extraHead := append([]clientFrame{}, frames...)
	extraHead[2].Snapshot = frames[1].Snapshot

	for name, set := range map[string][]clientFrame{
		"missing begin":                   without(0),
		"missing middle page":             without(2),
		"missing end":                     without(last),
		"pages out of order":              swapped,
		"repeated page":                   append(append(append([]clientFrame{}, frames[:3]...), frames[2]), frames[3:]...),
		"fewer than announced":            moreAnnounced,
		"end for another sequence":        wrongEnd,
		"page 0 without the installation": headless,
		"installation on a later page":    extraHead,
	} {
		if presented := feedAll(set); len(presented) != 0 {
			t.Errorf("%s: presented an incomplete set", name)
		}
	}
	// A new begin abandons the interrupted set; the complete one still shows.
	if presented := feedAll(append(append([]clientFrame{}, frames[:2]...), frames...)); len(presented) != 1 {
		t.Errorf("restarted set: presented %d snapshots, want 1", len(presented))
	}
}

func TestPagingRefusesWhatCannotFit(t *testing.T) {
	big := smallSnapshot(1)
	big.Portals[0].Name = strings.Repeat("n", 2000)
	if _, err := snapshotFrames(big, "", 1024); !service.IsCode(err, service.CodeCapacityExceeded) {
		t.Fatalf("a Portal larger than a page: %v", err)
	}
	if _, err := snapshotFrames(smallSnapshot(4*maxPages), "", 512); !service.IsCode(err, service.CodeCapacityExceeded) {
		t.Fatalf("more than %d pages: %v", maxPages, err)
	}
}

// socketPair is a connected client and server end with no service behind it.
func socketPair(t *testing.T) (*net.UnixConn, net.Conn) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	conns := make([]net.Conn, 2)
	for i, fd := range fds {
		file := os.NewFile(uintptr(fd), "socketpair")
		conns[i], err = net.FileConn(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conns[i].Close() })
	}
	return conns[0].(*net.UnixConn), conns[1]
}

func TestInterruptedSetIsNeverPresented(t *testing.T) {
	for _, complete := range []bool{false, true} {
		client, server := socketPair(t)
		go func() {
			if _, err := ipc.ReadFrame(server, ipc.MaxFrame); err != nil {
				return
			}
			payloads, _ := snapshotFrames(smallSnapshot(40), "", 1024)
			payloads = append([][]byte{[]byte(`{"type":"hello","version":1,"epoch":"e","capabilities":{"automaticDiscovery":"unavailable"}}`)}, payloads...)
			if !complete {
				payloads = payloads[:len(payloads)-1]
			}
			for _, p := range payloads {
				_ = ipc.WriteFrame(server, p)
			}
			server.Close()
		}()
		c, err := newClient(t.Context(), client, nil)
		if err != nil {
			t.Fatal(err)
		}
		var presented []service.Snapshot
		for s := range c.Snapshots() {
			presented = append(presented, s)
		}
		if want := map[bool]int{false: 0, true: 1}[complete]; len(presented) != want {
			t.Fatalf("complete=%v: presented %d snapshots, want %d", complete, len(presented), want)
		}
	}
}

// TestCallsHonourTheirContextWhileTheServerStopsReading: a server that
// stops reading cannot hold a call, or the calls queued behind it, past
// their own deadlines.
func TestCallsHonourTheirContextWhileTheServerStopsReading(t *testing.T) {
	client, server := socketPair(t)
	go func() {
		if _, err := ipc.ReadFrame(server, ipc.MaxFrame); err != nil {
			return
		}
		_ = ipc.WriteFrame(server, []byte(`{"type":"hello","version":1,"epoch":"e","capabilities":{"automaticDiscovery":"unavailable"}}`))
		// Then never read again.
	}()
	c, err := newClient(t.Context(), client, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	big := map[string]string{"name": strings.Repeat("x", 512<<10)}
	call := func(timeout time.Duration, m *Mutation) chan error {
		result := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(t.Context(), timeout)
			defer cancel()
			result <- c.Call(ctx, "createPortal", m, big, nil)
		}()
		return result
	}
	// The mutation's write stalls; the read queued behind it keeps its own
	// deadline, and the stalled mutation ends at its deadline as uncertain.
	start := time.Now()
	stalled := call(2*time.Second, &Mutation{RequestID: newUUID(), IssuedAt: time.Now()})
	waitFor(t, "the mutation to hold the write slot", func() bool { return len(c.writeSlot) == 1 })
	queued := call(100*time.Millisecond, nil)
	for _, want := range []struct {
		name   string
		result chan error
		err    error
		by     time.Duration
	}{{"queued read", queued, context.DeadlineExceeded, time.Second}, {"stalled mutation", stalled, ErrUncertain, 4 * time.Second}} {
		select {
		case err := <-want.result:
			if !errors.Is(err, want.err) || !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("%s: Call = %v, want %v at its deadline", want.name, err, want.err)
			}
			if elapsed := time.Since(start); elapsed > want.by {
				t.Errorf("%s returned after %v", want.name, elapsed)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s outlived its deadline behind a stalled write", want.name)
		}
	}
}

// TestEndedContextNeverClosesAHealthyConnection: a call whose ctx has
// already ended sends nothing and leaves the connection usable.
func TestEndedContextNeverClosesAHealthyConnection(t *testing.T) {
	h := newHarness(t)
	c := h.dial()
	ended, cancel := context.WithCancel(t.Context())
	cancel()
	for range 500 {
		if err := c.Call(ended, "getSnapshot", nil, nil, nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("Call = %v, want cancelled", err)
		}
	}
	snapshot(t, c)
}

func TestCloseDoesNotWaitForUndrainedChannels(t *testing.T) {
	snapshotFrame, _ := json.Marshal(snapshotFrame{Type: "snapshot", Snapshot: smallSnapshot(0)})
	eventFrame, _ := json.Marshal(enrolmentFrame{Type: "enrolment", Event: service.EnrolmentFailed, RequestID: newUUID(), PortalID: newUUID(), Nonce: newUUID()})
	for name, payload := range map[string][]byte{"Snapshots": snapshotFrame, "Enrolment": eventFrame} {
		client, server := socketPair(t)
		go func() {
			if _, err := ipc.ReadFrame(server, ipc.MaxFrame); err != nil {
				return
			}
			_ = ipc.WriteFrame(server, []byte(`{"type":"hello","version":1,"epoch":"e","capabilities":{"automaticDiscovery":"unavailable"}}`))
			// Until the client closes, so its channel fills and stays full.
			for ipc.WriteFrame(server, payload) == nil {
			}
		}()
		c, err := newClient(t.Context(), client, nil)
		if err != nil {
			t.Fatal(err)
		}
		full := func() bool { return len(c.Snapshots()) == cap(c.Snapshots()) }
		if name == "Enrolment" {
			full = func() bool { return len(c.Enrolment()) == cap(c.Enrolment()) }
		}
		waitFor(t, name+" full", full)
		closed := make(chan struct{})
		go func() {
			c.Close()
			close(closed)
		}()
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatalf("Close hung with %s undrained", name)
		}
	}
}
