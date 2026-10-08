package chunkclient

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
)

// Kind is the RESP3 type of a reply value.
type Kind int

const (
	KindSimple Kind = iota + 1
	KindInteger
	KindDouble
	KindBool
	KindNull
	KindBulk
	KindArray
	KindMap
)

// Value is one decoded RESP3 reply value.
type Value struct {
	Kind Kind
	// Text is a simple string, or the digits of an integer or double as the
	// server sent them (a double may be inf, -inf or nan). Integers keep
	// their text so that uN values above the int64 range lose nothing.
	Text  string
	Bool  bool
	Bulk  []byte
	Items []Value
	// Map holds the pairs of a map in reply order.
	Map []MapEntry
}

type MapEntry struct {
	Key   Value
	Value Value
}

// Int64 returns an integer value that fits int64.
func (v Value) Int64() (int64, error) {
	if v.Kind != KindInteger {
		return 0, fmt.Errorf("expected an integer, got %s", v.Kind)
	}
	return strconv.ParseInt(v.Text, 10, 64)
}

// Uint64 returns a non-negative integer value.
func (v Value) Uint64() (uint64, error) {
	if v.Kind != KindInteger {
		return 0, fmt.Errorf("expected an integer, got %s", v.Kind)
	}
	return strconv.ParseUint(v.Text, 10, 64)
}

// Float64 returns a double value.
func (v Value) Float64() (float64, error) {
	if v.Kind != KindDouble {
		return 0, fmt.Errorf("expected a double, got %s", v.Kind)
	}
	return parseDouble(v.Text)
}

// Lookup returns the value of a map entry whose key is the bulk or simple
// string key.
func (v Value) Lookup(key string) (Value, bool) {
	for _, entry := range v.Map {
		if (entry.Key.Kind == KindBulk && string(entry.Key.Bulk) == key) ||
			(entry.Key.Kind == KindSimple && entry.Key.Text == key) {
			return entry.Value, true
		}
	}
	return Value{}, false
}

func (k Kind) String() string {
	switch k {
	case KindSimple:
		return "simple string"
	case KindInteger:
		return "integer"
	case KindDouble:
		return "double"
	case KindBool:
		return "boolean"
	case KindNull:
		return "null"
	case KindBulk:
		return "bulk string"
	case KindArray:
		return "array"
	case KindMap:
		return "map"
	}
	return "unknown"
}

// ServerError is an `-ERR <CODE> <message>` reply.
type ServerError struct {
	Code    string
	Message string
}

func (e *ServerError) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + " " + e.Message
}

// The server errors of logins and rights, for errors.Is: a *ServerError
// matches the one with its code.
var (
	// ErrAuthRequired: the server needs a user to log in.
	ErrAuthRequired = &ServerError{Code: "AUTH_REQUIRED"}
	// ErrAuthFailed: the user or password is wrong.
	ErrAuthFailed = &ServerError{Code: "AUTH_FAILED"}
	// ErrPermissionDenied: the user lacks the right a statement needs.
	ErrPermissionDenied = &ServerError{Code: "PERMISSION_DENIED"}
)

// Is reports whether target is one of the code errors above with this
// error's code.
func (e *ServerError) Is(target error) bool {
	codeErr, ok := target.(*ServerError)
	return ok && codeErr.Message == "" && codeErr.Code == e.Code
}

// CurrentVersion returns the chunk version of a VERSION_MISMATCH error.
func (e *ServerError) CurrentVersion() (uint64, bool) {
	if e.Code != "VERSION_MISMATCH" {
		return 0, false
	}
	value, ok := strings.CutPrefix(e.Message, "current=")
	if !ok {
		return 0, false
	}
	version, err := strconv.ParseUint(value, 10, 64)
	return version, err == nil
}

const (
	// maxBulkBytes bounds one bulk string: the largest reply the server
	// sends (max_response_bytes, 64 MiB) with room for framing.
	maxBulkBytes = 256 << 20
	// maxNesting bounds how deep arrays and maps nest.
	maxNesting = 32
	// maxPrealloc caps the items allocated up front for an aggregate.
	maxPrealloc = 1024
)

// readReply reads one reply. A top-level `-ERR` reply is returned as a
// *ServerError; anything that does not parse is a protocol error.
func readReply(r *bufio.Reader) (Value, error) {
	return readValue(r, 0)
}

func readValue(r *bufio.Reader, depth int) (Value, error) {
	if depth > maxNesting {
		return Value{}, errors.New("reply nests too deeply")
	}
	line, err := readLine(r)
	if err != nil {
		return Value{}, err
	}
	if line == "" {
		return Value{}, errors.New("empty reply line")
	}
	body := line[1:]
	switch line[0] {
	case '+':
		return Value{Kind: KindSimple, Text: body}, nil
	case '-':
		if depth > 0 {
			return Value{}, fmt.Errorf("error inside an aggregate reply: %q", line)
		}
		return Value{}, parseServerError(body)
	case ':':
		if !validInteger(body) {
			return Value{}, fmt.Errorf("invalid integer %q", body)
		}
		return Value{Kind: KindInteger, Text: body}, nil
	case ',':
		if _, err := parseDouble(body); err != nil {
			return Value{}, fmt.Errorf("invalid double %q", body)
		}
		return Value{Kind: KindDouble, Text: body}, nil
	case '#':
		switch body {
		case "t":
			return Value{Kind: KindBool, Bool: true}, nil
		case "f":
			return Value{Kind: KindBool, Bool: false}, nil
		}
		return Value{}, fmt.Errorf("invalid boolean %q", line)
	case '_':
		if body != "" {
			return Value{}, fmt.Errorf("invalid null %q", line)
		}
		return Value{Kind: KindNull}, nil
	case '$':
		length, err := strconv.Atoi(body)
		if err != nil || length < 0 || length > maxBulkBytes {
			return Value{}, fmt.Errorf("invalid bulk length %q", body)
		}
		data := make([]byte, length+2)
		if _, err := io.ReadFull(r, data); err != nil {
			return Value{}, fmt.Errorf("read bulk string: %w", err)
		}
		if data[length] != '\r' || data[length+1] != '\n' {
			return Value{}, errors.New("bulk string is not terminated by CRLF")
		}
		return Value{Kind: KindBulk, Bulk: data[:length]}, nil
	case '*':
		count, err := aggregateCount(body)
		if err != nil {
			return Value{}, err
		}
		items := make([]Value, 0, min(count, maxPrealloc))
		for range count {
			item, err := readValue(r, depth+1)
			if err != nil {
				return Value{}, err
			}
			items = append(items, item)
		}
		return Value{Kind: KindArray, Items: items}, nil
	case '%':
		count, err := aggregateCount(body)
		if err != nil {
			return Value{}, err
		}
		entries := make([]MapEntry, 0, min(count, maxPrealloc))
		for range count {
			key, err := readValue(r, depth+1)
			if err != nil {
				return Value{}, err
			}
			value, err := readValue(r, depth+1)
			if err != nil {
				return Value{}, err
			}
			entries = append(entries, MapEntry{Key: key, Value: value})
		}
		return Value{Kind: KindMap, Map: entries}, nil
	}
	return Value{}, fmt.Errorf("unsupported reply type %q", line)
}

// readLine reads a CRLF-terminated line without its terminator.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", errors.New("connection closed by the server")
		}
		return "", fmt.Errorf("read reply: %w", err)
	}
	line, ok := strings.CutSuffix(line, "\r\n")
	if !ok {
		return "", fmt.Errorf("reply line is not terminated by CRLF: %q", line)
	}
	return line, nil
}

func aggregateCount(body string) (int, error) {
	count, err := strconv.Atoi(body)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("invalid aggregate length %q", body)
	}
	return count, nil
}

// validInteger accepts a decimal integer that fits int64 or uint64.
func validInteger(text string) bool {
	if _, err := strconv.ParseInt(text, 10, 64); err == nil {
		return true
	}
	_, err := strconv.ParseUint(text, 10, 64)
	return err == nil
}

func parseDouble(text string) (float64, error) {
	switch text {
	case "inf":
		return math.Inf(1), nil
	case "-inf":
		return math.Inf(-1), nil
	case "nan":
		return math.NaN(), nil
	}
	// Go also accepts forms such as "Inf" or "0x1p-2", which the server
	// never sends; refuse anything that is not a plain decimal number.
	if strings.ContainsAny(text, "xXpPiInN_") {
		return 0, fmt.Errorf("invalid double %q", text)
	}
	return strconv.ParseFloat(text, 64)
}

func parseServerError(body string) *ServerError {
	body = strings.TrimPrefix(body, "ERR ")
	code, message, _ := strings.Cut(body, " ")
	return &ServerError{Code: code, Message: message}
}
