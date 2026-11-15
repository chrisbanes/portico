package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"runtime"
	"strings"
	"testing"
)

func header(n uint32) []byte {
	return binary.BigEndian.AppendUint32(nil, n)
}

func TestReadFrameRejectsLengthsBeforeAllocating(t *testing.T) {
	for _, n := range []uint32{0, MaxFrame + 1, 0xFFFFFFFF} {
		reader := bytes.NewReader(header(n))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := ReadFrame(reader, MaxFrame)
		runtime.ReadMemStats(&after)
		if !errors.Is(err, ErrInvalidFrame) {
			t.Fatalf("length %d: ReadFrame = %v", n, err)
		}
		// Only the four-byte header may be allocated.
		if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64 {
			t.Errorf("length %d: %d bytes allocated before rejecting", n, allocated)
		}
	}
	payload := bytes.Repeat([]byte("x"), MaxFrame)
	var buf bytes.Buffer
	if err := WriteFrame(&buf, payload); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadFrame(&buf, MaxFrame); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("a full-size frame did not round-trip: %v", err)
	}
	if err := WriteFrame(&buf, append(payload, 'x')); !errors.Is(err, ErrInvalidFrame) {
		t.Errorf("WriteFrame over the limit = %v", err)
	}
}

func TestDecodeIsStrict(t *testing.T) {
	type inner struct {
		Port int    `json:"port"`
		Name string `json:"name"`
	}
	type message struct {
		Type   string `json:"type"`
		Params *inner `json:"params"`
		Nested any    `json:"nested"`
	}
	nest := func(depth int) string {
		return `{"type":"x","nested":` + strings.Repeat(`[`, depth-1) + strings.Repeat(`]`, depth-1) + `}`
	}
	accepted := []string{
		`{"type":"call","params":{"port":9007199254740991,"name":"a"}}`,
		`{"type":"x","params":{"port":-9007199254740991,"name":"` + strings.Repeat("a", 4096) + `"}}`,
		nest(16),
	}
	for _, input := range accepted {
		var m message
		if err := Decode([]byte(input), &m); err != nil {
			t.Errorf("Decode(%.60s) = %v, want accepted", input, err)
		}
	}
	rejected := map[string]string{
		"depth 17":          nest(17),
		"duplicate key":     `{"type":"a","type":"b"}`,
		"wrong-case member": `{"Type":"call"}`,
		"unknown field":     `{"type":"call","extra":1}`,
		"invalid UTF-8":     "{\"type\":\"\xff\"}",
		"2^53+1":            `{"type":"x","params":{"port":9007199254740993}}`,
		"long string":       `{"type":"` + strings.Repeat("a", 4097) + `"}`,
		"long member name":  `{"` + strings.Repeat("a", 4097) + `":1}`,
		"non-finite":        `{"type":"x","nested":1e400}`,
		"not an object":     `["type"]`,
		"trailing data":     `{"type":"x"} {}`,
		"truncated":         `{"type":`,
	}
	for name, input := range rejected {
		var m message
		if err := Decode([]byte(input), &m); !errors.Is(err, ErrInvalidJSON) {
			t.Errorf("%s: Decode = %v, want rejected", name, err)
		}
	}
}
