package ipc

import (
	"bytes"
	"math"
	"strconv"

	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
)

const (
	maxDepth  = 16
	maxString = 4 << 10
	// maxExactInteger is the largest integer JSON numbers carry exactly.
	maxExactInteger = 1<<53 - 1
)

// Decode strictly decodes one JSON object frame into v. It refuses nesting
// deeper than 16, duplicate or wrong-case or unknown members, invalid UTF-8,
// strings over 4 KiB, and numbers that are non-finite or beyond ±(2^53−1).
func Decode(data []byte, v any) error {
	if err := checkLimits(data); err != nil {
		return err
	}
	// Duplicate names and invalid UTF-8 are rejected by default, and member
	// names match case-sensitively.
	if err := json.Unmarshal(data, v, json.RejectUnknownMembers(true)); err != nil {
		return ErrInvalidJSON
	}
	return nil
}

func checkLimits(data []byte) error {
	decoder := jsontext.NewDecoder(bytes.NewReader(data))
	if decoder.PeekKind() != '{' {
		return ErrInvalidJSON
	}
	for {
		token, err := decoder.ReadToken()
		if err != nil {
			return ErrInvalidJSON
		}
		if decoder.StackDepth() > maxDepth {
			return ErrInvalidJSON
		}
		switch token.Kind() {
		case '"':
			if len(token.String()) > maxString {
				return ErrInvalidJSON
			}
		case '0':
			if !exactNumber(token.String()) {
				return ErrInvalidJSON
			}
		}
		if decoder.StackDepth() == 0 {
			// The object is complete; Unmarshal rejects anything after it.
			return nil
		}
	}
}

// exactNumber bounds every number, however it is spelled: 9007199254740993,
// 9007199254740993.0 and 9.007199254740993e15 all round past the exact range,
// so none can silently become a different integer. Overflow is an error.
func exactNumber(raw string) bool {
	value, err := strconv.ParseFloat(raw, 64)
	return err == nil && math.Abs(value) <= maxExactInteger
}
