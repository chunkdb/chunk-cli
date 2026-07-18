package main

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// encodeZrle is a minimal reference encoder used only to build test inputs; it
// mirrors the server codec closely enough to exercise decodeZrle.
func encodeZrle(input []byte) []byte {
	out := []byte{zrleCodecID}
	var size [4]byte
	binary.LittleEndian.PutUint32(size[:], uint32(len(input)))
	out = append(out, size[:]...)
	i := 0
	for i < len(input) {
		if input[i] == 0 {
			run := 1
			for i+run < len(input) && input[i+run] == 0 {
				run++
			}
			out = append(out, 0x00)
			out = appendUleb128(out, uint64(run))
			i += run
		} else {
			run := 1
			for i+run < len(input) && input[i+run] != 0 {
				run++
			}
			out = append(out, 0x01)
			out = appendUleb128(out, uint64(run))
			out = append(out, input[i:i+run]...)
			i += run
		}
	}
	return out
}

func appendUleb128(out []byte, v uint64) []byte {
	for v >= 0x80 {
		out = append(out, byte(v&0x7f)|0x80)
		v >>= 7
	}
	return append(out, byte(v))
}

func TestDecodeZrleRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5},
		append(append([]byte{1, 2}, make([]byte, 300)...), 9, 9),
		bytes.Repeat([]byte{0xAB}, 1000),
	}
	for _, want := range cases {
		got, err := decodeZrle(encodeZrle(want))
		if err != nil {
			t.Fatalf("decodeZrle(%d bytes) error: %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(want))
		}
	}
}

func TestDecodeZrleRejectsMalformed(t *testing.T) {
	valid := encodeZrle([]byte{1, 2, 3})
	cases := map[string][]byte{
		"too short":           {0x01, 0x00},
		"bad codec id":        {0x02, 0x03, 0x00, 0x00, 0x00, 0x01, 0x03, 1, 2, 3},
		"declared overflow":   {0x01, 0x01, 0x00, 0x00, 0x00, 0x01, 0x05, 1, 2, 3, 4, 5},
		"truncated literal":   {0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x03, 1},
		"declared too big":    {0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x01},
		"unknown token":       {0x01, 0x01, 0x00, 0x00, 0x00, 0x02, 0x01, 1},
		"produced under size": {0x01, 0x05, 0x00, 0x00, 0x00, 0x01, 0x03, 1, 2, 3},
	}
	for name, input := range cases {
		if _, err := decodeZrle(input); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	// The valid control input must still decode.
	if _, err := decodeZrle(valid); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}
