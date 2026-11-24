package chunkclient

import (
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// ServerError preserves known typed metadata and unknown diagnostic keys.
type ServerError struct {
	Code    string
	Message string
	Keys    map[string]string
}

func decodeErrorKey(value string) string {
	if decoded, err := url.PathUnescape(value); err == nil {
		return decoded
	}
	return value
}

func (e *ServerError) Error() string {
	parts := []string{e.Code}
	keys := make([]string, 0, len(e.Keys))
	for key := range e.Keys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		parts = append(parts, key+"="+strings.ReplaceAll(url.PathEscape(e.Keys[key]), "=", "%3D"))
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	return strings.Join(parts, " ")
}

// RetryClass depends on CODE alone. It never initiates an automatic retry.
func (e *ServerError) RetryClass() string {
	switch e.Code {
	case "VERSION_MISMATCH", "CONDITION_FAILED", "SCHEMA_MISMATCH":
		return "reread"
	case "CONFLICT":
		return "rerun"
	case "BUSY", "DISK_FULL", "RATE_LIMITED":
		return "backoff"
	case "OUTCOME_UNKNOWN":
		return "unknown_outcome"
	default:
		return "fatal"
	}
}

func (e *ServerError) ExitCode() int {
	switch e.RetryClass() {
	case "reread":
		return 2
	case "rerun":
		return 3
	case "backoff":
		return 4
	case "unknown_outcome":
		return 5
	default:
		return 1
	}
}

// UintKey reports a known numeric key only when it is a valid unsigned u64.
func (e *ServerError) UintKey(key string) (uint64, bool) {
	number, err := strconv.ParseUint(e.Keys[key], 10, 64)
	return number, err == nil
}

// OutcomeUnknownError is a connection lost after a request was written and
// before its reply: the request may have applied. Its retry class and exit
// status are those of OUTCOME_UNKNOWN. A transaction's statements before
// COMMIT never get it, since the server rolls them back when the
// connection closes.
type OutcomeUnknownError struct {
	Err error
}

func (e *OutcomeUnknownError) Error() string {
	return e.Err.Error() + "; the outcome is unknown: the statement was sent and may have applied"
}

func (e *OutcomeUnknownError) Unwrap() error { return e.Err }

func (e *OutcomeUnknownError) RetryClass() string { return "unknown_outcome" }

func (e *OutcomeUnknownError) ExitCode() int { return 5 }
