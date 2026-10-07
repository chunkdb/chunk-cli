package main

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

// extraEntryHeaderBytes is the block_index and bit_length each value of an
// EXTRA section starts with; a value also counts them against the chunk's
// extra data limit.
const extraEntryHeaderBytes = 8

// extraValue is one block's extra data (chunkdb docs/EXTRA_DATA.md). Bit n of
// the value is bit n%8 of byte n/8, least significant first.
type extraValue struct {
	block     int
	bitLength int
	bytes     []byte
}

func extraValueBytes(bitLength int) int { return (bitLength + 7) / 8 }

// decodeExtraSection splits the EXTRA section that follows the state in a
// CHUNKGET ... STATE EXTRA reply (and a CHUNKPUT ... STATE EXTRA body) into
// its values:
//
//	value := [block_index u32le][bit_length u32le][ceil(bit_length/8) bytes]
//
// in strictly ascending block index, each below blockCount and at least one
// bit long. An empty section has no values.
func decodeExtraSection(section []byte, blockCount int) ([]extraValue, error) {
	var values []extraValue
	for at := 0; at < len(section); {
		if len(section)-at < extraEntryHeaderBytes {
			return nil, errors.New("extra: value header extends past the section")
		}
		block := binary.LittleEndian.Uint32(section[at:])
		bitLength := binary.LittleEndian.Uint32(section[at+4:])
		if uint64(block) >= uint64(blockCount) {
			return nil, fmt.Errorf("extra: block index %d, the chunk has %d blocks", block, blockCount)
		}
		if len(values) > 0 && int(block) <= values[len(values)-1].block {
			return nil, fmt.Errorf("extra: block index %d after %d, not strictly ascending", block, values[len(values)-1].block)
		}
		if bitLength == 0 {
			return nil, fmt.Errorf("extra: block index %d has a value of 0 bits", block)
		}
		at += extraEntryHeaderBytes
		size := (uint64(bitLength) + 7) / 8
		if size > uint64(len(section)-at) {
			return nil, fmt.Errorf("extra: value of block index %d extends past the section", block)
		}
		values = append(values, extraValue{block: int(block), bitLength: int(bitLength), bytes: section[at : at+int(size)]})
		at += int(size)
	}
	return values, nil
}

// blockCoord is the world coordinate of local coordinate local in chunk
// coordinate c, for chunks size blocks wide (or high).
func blockCoord(c int64, size int, local int) (int64, error) {
	s := int64(size)
	if c > (math.MaxInt64-int64(local))/s || c < math.MinInt64/s {
		return 0, fmt.Errorf("chunk coordinate %d is out of range", c)
	}
	return c*s + int64(local), nil
}

// extraLines renders the values of chunk (cx, cy) one per line, with the
// block's world coordinates and its index in the chunk.
func extraLines(cx, cy string, g geometry, values []extraValue) ([]string, error) {
	chunkX, err := strconv.ParseInt(cx, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cx %q: %w", cx, err)
	}
	chunkY, err := strconv.ParseInt(cy, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid cy %q: %w", cy, err)
	}
	lines := make([]string, 0, len(values))
	for _, v := range values {
		x, err := blockCoord(chunkX, g.width, v.block%g.width)
		if err != nil {
			return nil, err
		}
		y, err := blockCoord(chunkY, g.height, v.block/g.width)
		if err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("x=%d y=%d block=%d bit_length=%d hex=%s",
			x, y, v.block, v.bitLength, hex.EncodeToString(v.bytes)))
	}
	return lines, nil
}

// extraCap is max_extra_chunk_bytes from HELLO: the most extra data any chunk
// can hold. It bounds XPUT values and EXTRA sections; the server closes the
// connection on a payload above it.
func (s *session) extraCap() (int, error) {
	if s.maxExtraChunkBytes <= 0 {
		return 0, errors.New("the server does not support extra data (its HELLO reply has no max_extra_chunk_bytes)")
	}
	return s.maxExtraChunkBytes, nil
}

// protectNegativeArgs inserts "--" before the first argument that is a
// negative integer and not the value of one of valueFlags, so the flag
// package reads negative coordinates as arguments instead of unknown flags.
func protectNegativeArgs(args []string, valueFlags ...string) []string {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || !strings.HasPrefix(arg, "-") {
			return args
		}
		if _, err := strconv.ParseInt(arg, 10, 64); err == nil {
			return slices.Concat(args[:i], []string{"--"}, args[i:])
		}
		if name := strings.TrimLeft(arg, "-"); slices.Contains(valueFlags, name) {
			i++
		}
	}
	return args
}

type xGetRequest struct {
	x, y string
	bits bool
}

func parseXGetArgs(cmdArgs []string, stderr io.Writer) (xGetRequest, error) {
	fs := flag.NewFlagSet("xget", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req xGetRequest
	fs.BoolVar(&req.bits, "bits", false, "print the value as 0/1 text")
	if err := fs.Parse(protectNegativeArgs(cmdArgs)); err != nil {
		return xGetRequest{}, err
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		return xGetRequest{}, errors.New("usage: xget [--bits] <x> <y>")
	}
	if err := intArgsPrefix(remaining, "x", "y"); err != nil {
		return xGetRequest{}, err
	}
	req.x, req.y = remaining[0], remaining[1]
	return req, nil
}

// runXGet prints a block's extra data: "bit_length=<n>" and the bytes as hex,
// with --bits the 0/1 text, or "(none)" for a block without a value.
func runXGet(s *session, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	req, err := parseXGetArgs(cmdArgs, stderr)
	if err != nil {
		return err
	}
	reply, err := runBulkOrNull(s.client, fmt.Sprintf("XGET %s %s", req.x, req.y))
	if err != nil {
		return err
	}
	if reply == nil {
		fmt.Fprintln(stdout, "(none)")
		return nil
	}
	if len(reply) < 4 {
		return fmt.Errorf("xget failed: reply of %d bytes has no bit length", len(reply))
	}
	bitLength := binary.LittleEndian.Uint32(reply)
	value := reply[4:]
	if bitLength == 0 || uint64(len(value)) != (uint64(bitLength)+7)/8 {
		return fmt.Errorf("xget failed: %d value bytes for %d bits", len(value), bitLength)
	}
	if req.bits {
		fmt.Fprintln(stdout, bitsFromBytes(value, int(bitLength)))
		return nil
	}
	fmt.Fprintf(stdout, "bit_length=%d\n%s\n", bitLength, hex.EncodeToString(value))
	return nil
}

type xPutRequest struct {
	x, y      string
	bitLength int
	// data is nil when it comes from --in and is read at run time.
	data   []byte
	inPath string
}

func parseXPutArgs(cmdArgs []string, stderr io.Writer) (xPutRequest, error) {
	usage := errors.New("usage: xput <x> <y> <bits> | xput --hex --bit-length <n> <x> <y> <hex> | xput --bit-length <n> --in <file> <x> <y>")
	fs := flag.NewFlagSet("xput", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req xPutRequest
	var hexValue bool
	var bitLength string
	fs.BoolVar(&hexValue, "hex", false, "the value is hex bytes")
	fs.StringVar(&bitLength, "bit-length", "", "the value's length in bits (with --hex or --in)")
	fs.StringVar(&req.inPath, "in", "", "read the value's bytes from file")
	if err := fs.Parse(protectNegativeArgs(cmdArgs, "bit-length", "in")); err != nil {
		return xPutRequest{}, err
	}
	remaining := fs.Args()
	switch {
	case req.inPath != "" && hexValue:
		return xPutRequest{}, usage
	case req.inPath != "" && len(remaining) == 2:
	case req.inPath == "" && len(remaining) == 3:
	default:
		return xPutRequest{}, usage
	}
	if err := intArgsPrefix(remaining, "x", "y"); err != nil {
		return xPutRequest{}, err
	}
	req.x, req.y = remaining[0], remaining[1]

	if !hexValue && req.inPath == "" {
		if bitLength != "" {
			return xPutRequest{}, errors.New("--bit-length goes with --hex or --in; <bits> gives its own length")
		}
		if err := validateNonEmptyBits(remaining[2]); err != nil {
			return xPutRequest{}, err
		}
		req.bitLength = len(remaining[2])
		req.data = bytesFromBits(remaining[2])
		return req, nil
	}
	if bitLength == "" {
		return xPutRequest{}, errors.New("--hex and --in need --bit-length <n>")
	}
	n, err := strconv.ParseUint(bitLength, 10, 32)
	if err != nil || n == 0 {
		return xPutRequest{}, fmt.Errorf("invalid bit length %q: must be a positive 32-bit integer", bitLength)
	}
	req.bitLength = int(n)
	if hexValue {
		data, err := hex.DecodeString(remaining[2])
		if err != nil {
			return xPutRequest{}, fmt.Errorf("value must be hex: %w", err)
		}
		if err := checkExtraValueSize(data, req.bitLength); err != nil {
			return xPutRequest{}, err
		}
		req.data = data
	}
	return req, nil
}

func checkExtraValueSize(data []byte, bitLength int) error {
	if len(data) != extraValueBytes(bitLength) {
		return fmt.Errorf("a value of %d bits takes %d bytes, got %d", bitLength, extraValueBytes(bitLength), len(data))
	}
	return nil
}

// runXPut sets a block's extra data, given as 0/1 text or as bytes (hex or a
// file) with an explicit bit length, and prints the reply.
func runXPut(s *session, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	req, err := parseXPutArgs(cmdArgs, stderr)
	if err != nil {
		return err
	}
	// Without a table the server refuses the payload unread and closes the
	// connection.
	if _, err := s.requireGeometry(); err != nil {
		return err
	}
	limit, err := s.extraCap()
	if err != nil {
		return err
	}
	if req.inPath != "" {
		info, err := os.Stat(req.inPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", req.inPath, err)
		}
		if info.Size() != int64(extraValueBytes(req.bitLength)) {
			return fmt.Errorf("a value of %d bits takes %d bytes, %s has %d",
				req.bitLength, extraValueBytes(req.bitLength), req.inPath, info.Size())
		}
		if req.data, err = os.ReadFile(req.inPath); err != nil {
			return fmt.Errorf("read %s: %w", req.inPath, err)
		}
		if err := checkExtraValueSize(req.data, req.bitLength); err != nil {
			return err
		}
	}
	if len(req.data) > limit-extraEntryHeaderBytes {
		return fmt.Errorf("xput failed: a value of %d bytes exceeds the server's largest value (%d bytes: max_extra_chunk_bytes minus %d)",
			len(req.data), limit-extraEntryHeaderBytes, extraEntryHeaderBytes)
	}
	resp, err := s.client.CommandWithPayload(fmt.Sprintf("XPUT %s %s %d %d", req.x, req.y, req.bitLength, len(req.data)), req.data)
	if err != nil {
		return fmt.Errorf("xput failed: %w", err)
	}
	if resp.Kind != chunkclient.ResponseSimple {
		return errors.New("xput failed: expected simple response")
	}
	fmt.Fprintln(stdout, resp.Simple)
	return nil
}
