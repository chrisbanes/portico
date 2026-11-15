package ipc

import (
	"encoding/binary"
	"io"
)

// MaxFrame is the largest frame payload in bytes.
const MaxFrame = 1 << 20

// ReadFrame reads one four-byte big-endian length and its payload, refusing a
// length outside 1..limit before allocating anything for it.
func ReadFrame(r io.Reader, limit int) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	return readPayload(r, header, limit)
}

func readPayload(r io.Reader, header [4]byte, limit int) ([]byte, error) {
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || uint64(n) > uint64(limit) {
		return nil, ErrInvalidFrame
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// WriteFrame writes payload as one frame.
func WriteFrame(w io.Writer, payload []byte) error {
	if len(payload) == 0 || len(payload) > MaxFrame {
		return ErrInvalidFrame
	}
	frame := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(frame, uint32(len(payload)))
	copy(frame[4:], payload)
	_, err := w.Write(frame)
	return err
}
