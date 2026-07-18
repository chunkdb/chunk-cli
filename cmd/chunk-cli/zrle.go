package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// zrleCodecID is the leading byte of a zrle-compressed payload, matching the
// server's codec (see chunkdb docs/STORAGE_FORMAT.md).
const zrleCodecID = 0x01

// maxZrleOutputBytes bounds decompression so a hostile or corrupt declared
// size cannot make the CLI allocate unbounded memory. It matches the server's
// hard response-size limit for chunk state transfers.
const maxZrleOutputBytes = 64 * 1024 * 1024

// decodeZrle reverses the server's zero-run-length codec:
//
//	[codec_id u8 = 0x01][uncompressed_size u32le][token...]
//	token := 0x00 <uleb128 n>            n zero bytes
//	       | 0x01 <uleb128 n> <n bytes>  n literal bytes
//
// It rejects truncated, malformed, or oversized input and any payload whose
// produced size differs from the declared size.
func decodeZrle(data []byte) ([]byte, error) {
	if len(data) < 5 {
		return nil, errors.New("zrle: input too small")
	}
	if data[0] != zrleCodecID {
		return nil, fmt.Errorf("zrle: unsupported codec id 0x%02x", data[0])
	}
	declared := binary.LittleEndian.Uint32(data[1:5])
	if declared > maxZrleOutputBytes {
		return nil, fmt.Errorf("zrle: declared size %d exceeds %d-byte limit", declared, maxZrleOutputBytes)
	}

	out := make([]byte, 0, declared)
	cursor := 5
	for cursor < len(data) {
		token := data[cursor]
		cursor++
		run, next, err := readUleb128(data, cursor)
		if err != nil {
			return nil, err
		}
		cursor = next
		if run == 0 {
			return nil, errors.New("zrle: zero-length run")
		}
		if run > uint64(declared)-uint64(len(out)) {
			return nil, errors.New("zrle: output overflows declared size")
		}
		switch token {
		case 0x00:
			out = append(out, make([]byte, run)...)
		case 0x01:
			if run > uint64(len(data)-cursor) {
				return nil, errors.New("zrle: truncated literal run")
			}
			out = append(out, data[cursor:cursor+int(run)]...)
			cursor += int(run)
		default:
			return nil, fmt.Errorf("zrle: unknown token 0x%02x", token)
		}
	}
	if uint32(len(out)) != declared {
		return nil, fmt.Errorf("zrle: produced %d bytes, declared %d", len(out), declared)
	}
	return out, nil
}

func readUleb128(data []byte, cursor int) (uint64, int, error) {
	var value uint64
	var shift uint
	for {
		if cursor >= len(data) {
			return 0, 0, errors.New("zrle: truncated varint")
		}
		if shift >= 63 {
			return 0, 0, errors.New("zrle: varint too large")
		}
		b := data[cursor]
		cursor++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, cursor, nil
		}
		shift += 7
	}
}
