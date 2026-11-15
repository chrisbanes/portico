package management

import (
	"encoding/json"

	"github.com/chrisbanes/portico/helper/internal/ipc"
	"github.com/chrisbanes/portico/helper/internal/service"
)

// maxPages bounds a paged snapshot set. With frames of at most 1 MiB it
// also keeps the set within the contract's 64 MiB.
const maxPages = 64

// enqueueSnapshot is the engine's subscription callback. It runs under the
// engine lock, so it only queues: a full queue disconnects this client
// rather than delaying the engine.
func (c *conn) enqueueSnapshot(s service.Snapshot) {
	select {
	case c.snapshots <- s:
	default:
		c.overflow()
	}
}

func (c *conn) overflow() { c.fullOnce.Do(func() { close(c.full) }) }

// pump encodes and sends this connection's snapshots and enrolment events,
// every snapshot in sequence order with none skipped or merged.
func (c *conn) pump() {
	var snapshots chan service.Snapshot
	ready := c.ready
	for {
		select {
		case <-c.ic.Done():
			return
		case <-c.full:
			c.closeWith(&Error{Code: ipc.CodeSlowConsumer, Retryable: true}, nil)
			return
		case <-ready:
			snapshots, ready = c.snapshots, nil
		case s := <-snapshots:
			frames, err := snapshotFrames(s, "", c.ic.MaxFrame())
			if err != nil {
				c.closeWith(wireError(err), nil)
				return
			}
			if err := c.send(frames...); err != nil {
				c.failed(err)
				return
			}
		case event := <-c.events:
			if c.park(event) {
				continue
			}
			if err := c.sendJSON(enrolmentEventFrame(event)); err != nil {
				c.failed(err)
				return
			}
		}
	}
}

// snapshotFrames encodes a snapshot as one frame when it fits, and otherwise
// as a complete paged set. callID marks a getSnapshot reply. A set that
// cannot fit is refused whole, so nothing partial is ever sent.
func snapshotFrames(s service.Snapshot, callID string, maxFrame int) ([][]byte, error) {
	var single any = snapshotFrame{Type: "snapshot", Snapshot: s}
	if callID != "" {
		single = replyFrame{Type: "reply", CallID: callID, Result: s}
	}
	payload, err := json.Marshal(single)
	if err != nil {
		return nil, err
	}
	if len(payload) <= maxFrame {
		return [][]byte{payload}, nil
	}

	// Each Portal is encoded once; pages are packed greedily by size.
	portals := make([]json.RawMessage, len(s.Portals))
	for i, p := range s.Portals {
		if portals[i], err = json.Marshal(p); err != nil {
			return nil, err
		}
	}
	head := s
	head.Portals = []service.PortalSnapshot{}
	tooLarge := &service.Error{Code: service.CodeCapacityExceeded}
	var pages [][]byte
	for next := 0; next < len(portals) || len(pages) == 0; {
		if len(pages) == maxPages {
			return nil, tooLarge
		}
		page := snapshotPage{Type: "snapshotPage", CallID: callID, PageIndex: len(pages), Portals: []json.RawMessage{}}
		if len(pages) == 0 {
			page.Snapshot = &head
		}
		bare, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		size, start := len(bare), next
		for ; next < len(portals); next++ {
			add := len(portals[next])
			if next > start {
				add++ // the separating comma
			}
			if size+add > maxFrame {
				break
			}
			size += add
		}
		if next == start && next < len(portals) {
			return nil, tooLarge
		}
		page.Portals = portals[start:next]
		frame, err := json.Marshal(page)
		if err != nil {
			return nil, err
		}
		if len(frame) > maxFrame {
			return nil, tooLarge
		}
		pages = append(pages, frame)
	}
	begin, err := json.Marshal(snapshotBegin{Type: "snapshotBegin", CallID: callID, Epoch: s.Epoch, Sequence: s.Sequence, Revision: s.Revision, PageCount: len(pages)})
	if err != nil {
		return nil, err
	}
	end, err := json.Marshal(snapshotEnd{Type: "snapshotEnd", CallID: callID, Sequence: s.Sequence})
	if err != nil {
		return nil, err
	}
	return append(append([][]byte{begin}, pages...), end), nil
}
