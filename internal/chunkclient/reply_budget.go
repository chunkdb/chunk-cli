package chunkclient

import (
	"bufio"
	"fmt"
	"strings"
	"unsafe"
)

// ReplyLimits bounds successful area replies on this request only.
// Error replies and unrelated pushes retain their ordinary framing contract.
type ReplyLimits struct {
	MaxBytes      uint64
	MaxAreaChunks uint64
	Page          bool
	WithSchema    bool
	UnknownShape  bool
}

type replyLimitError struct{ message string }

func (e *replyLimitError) Error() string { return e.message }

type replyShape uint8

const (
	replyShapeNone replyShape = iota
	replyShapeRows
	replyShapePage
	replyShapeSchema
)

type replyBudget struct {
	limits    ReplyLimits
	remaining uint64
	slots     uint64
}

func (b *replyBudget) exceed(reason string) error {
	return &replyLimitError{fmt.Sprintf("GET AREA reply exceeds its negotiated %d-byte or %d-cell bound: %s", b.limits.MaxBytes, b.limits.MaxAreaChunks, reason)}
}

func (b *replyBudget) consume(bytes uint64) error {
	if bytes > b.remaining {
		return b.exceed("encoded response bytes")
	}
	b.remaining -= bytes
	return nil
}

func (b *replyBudget) aggregate(count uint64, pairs bool, shape replyShape) error {
	if shape == replyShapeRows && count > b.limits.MaxAreaChunks {
		return b.exceed("chunk array cardinality")
	}
	multiplier := uint64(1)
	if pairs {
		multiplier = 2
	}
	if count > b.remaining/(3*multiplier) {
		return b.exceed("aggregate cannot fit its remaining encoded bytes")
	}
	if count > b.slots/multiplier {
		return b.exceed("decoded reply aggregate storage")
	}
	b.slots -= count * multiplier
	return nil
}

func (b *replyBudget) line(reader *bufio.Reader) (string, error) {
	var line []byte
	for {
		if b.remaining == 0 {
			return "", b.exceed("unterminated reply line")
		}
		value, err := reader.ReadByte()
		if err != nil {
			return "", fmt.Errorf("read reply: %w", err)
		}
		if err := b.consume(1); err != nil {
			return "", err
		}
		line = append(line, value)
		if value == '\n' {
			text, valid := strings.CutSuffix(string(line), "\r\n")
			if !valid {
				return "", fmt.Errorf("reply line is not terminated by CRLF")
			}
			return text, nil
		}
	}
}

func readReplyLimited(reader *bufio.Reader, limits *ReplyLimits) (Value, error) {
	if limits == nil {
		return readReply(reader)
	}
	prefix, err := reader.Peek(1)
	if err != nil {
		return Value{}, err
	}
	if prefix[0] == '-' || prefix[0] == '>' {
		return readReply(reader)
	}
	budget := &replyBudget{limits: *limits, remaining: limits.MaxBytes, slots: (64 * 1024 * 1024) / uint64(unsafe.Sizeof(Value{}))}
	shape := replyShapeRows
	if limits.Page {
		shape = replyShapePage
	}
	if limits.WithSchema {
		shape = replyShapeSchema
	}
	if limits.UnknownShape {
		shape = replyShapeNone
	}
	return readValueLimited(reader, 0, budget, shape)
}
