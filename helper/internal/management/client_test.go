package management

import (
	"encoding/json"
	"net"
	"os"
	"reflect"
	"strings"
	"syscall"
	"testing"

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
