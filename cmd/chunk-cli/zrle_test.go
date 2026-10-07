package main

import (
	"bytes"
	"testing"
)

func TestZrleRoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5},
		append(append([]byte{1, 2}, make([]byte, 300)...), 9, 9),
		bytes.Repeat([]byte{0xAB}, 1000),
	}
	for _, want := range cases {
		got, err := decodeZrle(encodeZrle(want), len(want))
		if err != nil {
			t.Fatalf("decodeZrle(%d bytes) error: %v", len(want), err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("round trip mismatch: got %d bytes, want %d", len(got), len(want))
		}
	}
	sparse := make([]byte, 512)
	sparse[7] = 0x5a
	if encoded := encodeZrle(sparse); len(encoded) >= len(sparse)/8 {
		t.Fatalf("sparse data did not compress: %d bytes", len(encoded))
	}
}

// Data the run encoding would expand costs at most zrleMaxOverheadBytes.
func TestEncodeZrleBoundsExpansion(t *testing.T) {
	for _, period := range []int{2, 3, 4, 5} {
		input := make([]byte, 65536)
		for i := 0; i < len(input); i += period {
			input[i] = 0x5a
		}
		encoded := encodeZrle(input)
		if len(encoded) > len(input)+zrleMaxOverheadBytes {
			t.Fatalf("period %d: %d bytes for %d", period, len(encoded), len(input))
		}
		got, err := decodeZrle(encoded, len(input))
		if err != nil || !bytes.Equal(got, input) {
			t.Fatalf("period %d: round trip failed: %v", period, err)
		}
	}
}

func TestDecodeZrleRejectsMalformed(t *testing.T) {
	valid := encodeZrle([]byte{1, 2, 3})
	cases := map[string][]byte{
		"too short":           {0x01, 0x00},
		"bad codec id":        {0x02, 0x03, 0x00, 0x00, 0x00, 0x01, 0x03, 1, 2, 3},
		"declared overflow":   {0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x05, 1, 2, 3, 4, 5},
		"truncated literal":   {0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x03, 1},
		"declared too big":    {0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x01},
		"unknown token":       {0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0x01, 1},
		"produced under size": {0x01, 0x03, 0x00, 0x00, 0x00, 0x01, 0x02, 1, 2},
	}
	for name, input := range cases {
		if _, err := decodeZrle(input, 3); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
	if _, err := decodeZrle(valid, 4); err == nil {
		t.Fatalf("a size other than the expected one was accepted")
	}
	if _, err := decodeZrle(valid, 3); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
}

// A STATE EXTRA reply decodes to the state size plus up to the extra data cap.
func TestDecodeZrleRange(t *testing.T) {
	data := append([]byte{1, 2, 3}, make([]byte, 40)...)
	encoded := encodeZrle(data)
	for _, bounds := range [][2]int{{3, 43}, {43, 43}, {3, 1000}} {
		got, err := decodeZrleRange(encoded, bounds[0], bounds[1])
		if err != nil || !bytes.Equal(got, data) {
			t.Fatalf("bounds %v: got %d bytes, %v", bounds, len(got), err)
		}
	}
	for _, bounds := range [][2]int{{44, 100}, {3, 42}} {
		if _, err := decodeZrleRange(encoded, bounds[0], bounds[1]); err == nil {
			t.Fatalf("bounds %v: a size outside them was accepted", bounds)
		}
	}
}
