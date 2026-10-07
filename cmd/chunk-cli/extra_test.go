package main

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"
	"reflect"
	"strings"
	"testing"
)

// encodeExtraSection builds an EXTRA section from values in ascending block
// order, as the server sends it.
func encodeExtraSection(values []extraValue) []byte {
	var out []byte
	for _, v := range values {
		out = binary.LittleEndian.AppendUint32(out, uint32(v.block))
		out = binary.LittleEndian.AppendUint32(out, uint32(v.bitLength))
		out = append(out, v.bytes...)
	}
	return out
}

// Bit n of a value is bit n%8 of byte n/8, so the text "1011" is 0x0d.
func TestExtraValueBitOrder(t *testing.T) {
	cases := map[string][]byte{
		"1":            {0x01},
		"1011":         {0x0d},
		"00000001":     {0x80},
		"101100000001": {0x0d, 0x08},
		"000000001":    {0x00, 0x01},
	}
	for bits, want := range cases {
		got := bytesFromBits(bits)
		if !bytes.Equal(got, want) {
			t.Fatalf("bytesFromBits(%q) = % x, want % x", bits, got, want)
		}
		if back := bitsFromBytes(got, len(bits)); back != bits {
			t.Fatalf("bitsFromBytes(% x, %d) = %q, want %q", got, len(bits), back, bits)
		}
	}
}

func TestDecodeExtraSection(t *testing.T) {
	values := []extraValue{
		{block: 0, bitLength: 1, bytes: []byte{0x01}},
		{block: 2, bitLength: 12, bytes: []byte{0x0d, 0x08}},
		{block: 3, bitLength: 16, bytes: []byte{0xff, 0x7f}},
	}
	section := encodeExtraSection(values)
	got, err := decodeExtraSection(section, 4)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(got, values) {
		t.Fatalf("got %+v, want %+v", got, values)
	}
	if got, err := decodeExtraSection(nil, 4); err != nil || len(got) != 0 {
		t.Fatalf("empty section: got %+v, %v", got, err)
	}

	value := func(block, bitLength uint32, data ...byte) []byte {
		out := binary.LittleEndian.AppendUint32(nil, block)
		out = binary.LittleEndian.AppendUint32(out, bitLength)
		return append(out, data...)
	}
	malformed := map[string][]byte{
		"truncated header":     section[:5],
		"truncated value":      value(1, 12, 0x0d),
		"block out of range":   value(4, 1, 0x01),
		"zero bits":            value(1, 0),
		"repeated block":       append(value(1, 1, 0x01), value(1, 1, 0x01)...),
		"descending blocks":    append(value(2, 1, 0x01), value(1, 1, 0x01)...),
		"huge bit length":      value(1, math.MaxUint32, 0x01),
		"trailing header only": append(value(1, 1, 0x01), 0x02, 0x00, 0x00, 0x00),
	}
	for name, input := range malformed {
		if _, err := decodeExtraSection(input, 4); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}

func TestBlockCoord(t *testing.T) {
	cases := []struct {
		c     int64
		size  int
		local int
		want  int64
	}{
		{0, 2, 1, 1}, {3, 16, 5, 53}, {-1, 2, 1, -1}, {-1, 2, 0, -2}, {-4, 16, 15, -49},
		{math.MaxInt64 / 16, 16, 15, math.MaxInt64/16*16 + 15}, {math.MinInt64 / 16, 16, 0, math.MinInt64},
	}
	for _, tc := range cases {
		got, err := blockCoord(tc.c, tc.size, tc.local)
		if err != nil || got != tc.want {
			t.Fatalf("blockCoord(%d, %d, %d) = %d, %v; want %d", tc.c, tc.size, tc.local, got, err, tc.want)
		}
	}
	if _, err := blockCoord(math.MaxInt64/16+1, 16, 0); err == nil {
		t.Fatalf("expected an overflow error")
	}
	if _, err := blockCoord(math.MinInt64/16-1, 16, 15); err == nil {
		t.Fatalf("expected an underflow error")
	}
}

func TestExtraLines(t *testing.T) {
	g := geometry{blockBits: 4, width: 2, height: 2}
	values := []extraValue{{block: 1, bitLength: 12, bytes: []byte{0x0d, 0x08}}, {block: 2, bitLength: 3, bytes: []byte{0x07}}}
	lines, err := extraLines("-1", "3", g, values)
	if err != nil {
		t.Fatalf("extraLines: %v", err)
	}
	want := "x=-1 y=6 block=1 bit_length=12 hex=0d08\nx=-2 y=7 block=2 bit_length=3 hex=07"
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestProtectNegativeArgs(t *testing.T) {
	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"-1", "2"}, []string{"--", "-1", "2"}},
		{[]string{"--bits", "-1", "-2"}, []string{"--bits", "--", "-1", "-2"}},
		{[]string{"1", "-2"}, []string{"1", "-2"}},
		{[]string{"--bit-length", "12", "--hex", "-3", "4", "0d08"}, []string{"--bit-length", "12", "--hex", "--", "-3", "4", "0d08"}},
		{[]string{"--in", "-5", "0", "0"}, []string{"--in", "-5", "0", "0"}},
		{[]string{"--", "-1", "2"}, []string{"--", "-1", "2"}},
		{[]string{"--nope", "-1"}, []string{"--nope", "--", "-1"}},
	}
	for _, tc := range cases {
		got := protectNegativeArgs(tc.args, "bit-length", "in")
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("protectNegativeArgs(%q) = %q, want %q", tc.args, got, tc.want)
		}
	}
}

func TestParseXPutArgs(t *testing.T) {
	req, err := parseXPutArgs([]string{"-3", "4", "101100000001"}, io.Discard)
	if err != nil {
		t.Fatalf("bits form: %v", err)
	}
	if req.x != "-3" || req.y != "4" || req.bitLength != 12 || !bytes.Equal(req.data, []byte{0x0d, 0x08}) {
		t.Fatalf("bits form: got %+v", req)
	}
	req, err = parseXPutArgs([]string{"--hex", "--bit-length", "12", "-3", "-4", "0d08"}, io.Discard)
	if err != nil {
		t.Fatalf("hex form: %v", err)
	}
	if req.x != "-3" || req.y != "-4" || req.bitLength != 12 || !bytes.Equal(req.data, []byte{0x0d, 0x08}) {
		t.Fatalf("hex form: got %+v", req)
	}
	req, err = parseXPutArgs([]string{"--bit-length", "9", "--in", "value.bin", "0", "0"}, io.Discard)
	if err != nil {
		t.Fatalf("file form: %v", err)
	}
	if req.inPath != "value.bin" || req.bitLength != 9 || req.data != nil {
		t.Fatalf("file form: got %+v", req)
	}
}
