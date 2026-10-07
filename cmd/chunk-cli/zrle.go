package main

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// zrleCodecID is the leading byte of a zrle-compressed payload, matching the
// server's codec (see chunkdb docs/STORAGE_FORMAT.md).
const zrleCodecID = 0x01

// zrleMaxOverheadBytes is the most encodeZrle adds to its input: the header
// and one literal token over the whole input. The server accepts a CHUNKPUT
// ZRLE payload up to 16 bytes over the raw size.
const zrleMaxOverheadBytes = 5 + 1 + 5

// encodeZrle produces the server's zero-run-length encoding. Data that the
// run encoding would expand is emitted as one literal token.
func encodeZrle(input []byte) []byte {
	out := []byte{zrleCodecID}
	out = binary.LittleEndian.AppendUint32(out, uint32(len(input)))
	header := len(out)
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
			continue
		}
		// Short zero gaps (up to 2 bytes) stay inside the literal run.
		run := 1
		for i+run < len(input) {
			if input[i+run] != 0 {
				run++
				continue
			}
			zeros := 0
			for i+run+zeros < len(input) && input[i+run+zeros] == 0 {
				zeros++
			}
			if zeros <= 2 && i+run+zeros < len(input) {
				run += zeros + 1
				continue
			}
			break
		}
		out = append(out, 0x01)
		out = appendUleb128(out, uint64(run))
		out = append(out, input[i:i+run]...)
		i += run
	}
	literal := append([]byte(nil), out[:header]...)
	if len(input) > 0 {
		literal = append(literal, 0x01)
		literal = appendUleb128(literal, uint64(len(input)))
	}
	if len(out) > len(literal)+len(input) {
		return append(literal, input...)
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

// decodeZrle reverses the server's zero-run-length codec:
//
//	[codec_id u8 = 0x01][uncompressed_size u32le][token...]
//	token := 0x00 <uleb128 n>            n zero bytes
//	       | 0x01 <uleb128 n> <n bytes>  n literal bytes
//
// expected is the size the table's geometry gives the data. It rejects
// truncated or malformed input and any payload that declares or produces a
// different size, so a hostile size cannot make the CLI allocate more.
func decodeZrle(data []byte, expected int) ([]byte, error) {
	return decodeZrleRange(data, expected, expected)
}

// decodeZrleRange is decodeZrle for data of minSize to maxSize bytes, such as
// a chunk state followed by an EXTRA section.
func decodeZrleRange(data []byte, minSize int, maxSize int) ([]byte, error) {
	if len(data) < 5 {
		return nil, errors.New("zrle: input too small")
	}
	if data[0] != zrleCodecID {
		return nil, fmt.Errorf("zrle: unsupported codec id 0x%02x", data[0])
	}
	declared := binary.LittleEndian.Uint32(data[1:5])
	if minSize == maxSize && uint64(declared) != uint64(minSize) {
		return nil, fmt.Errorf("zrle: declared size %d, expected %d", declared, minSize)
	}
	if uint64(declared) < uint64(minSize) || uint64(declared) > uint64(maxSize) {
		return nil, fmt.Errorf("zrle: declared size %d, expected %d..%d", declared, minSize, maxSize)
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
