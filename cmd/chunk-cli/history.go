package main

import (
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Block history (chunkdb docs/HISTORY.md): tags on writes (--tag), reads in
// the past (--at, --at-time) and the history listings.

// tagCommands take --tag: the writes without flags of their own. chunkput,
// chunkbatch and xput register it with their other flags.
var tagCommands = map[string]bool{
	"set": true, "unset": true, "mset": true, "xdel": true, "chunkset": true, "chunksetstate": true,
}

// pastCommands take --at or --at-time: the reads without flags of their own.
// chunkget registers them with its other flags.
var pastCommands = map[string]bool{
	"get": true, "chunk": true, "chunkstate": true, "chunkrange": true, "chunkradius": true,
}

// historyListCoords are the coordinates of the history listings.
var historyListCoords = map[string][]string{
	"history":      {"x", "y"},
	"chunkhistory": {"cx", "cy"},
	"rangehistory": {"cx0", "cy0", "cx1", "cy1"},
}

const historyListOptions = "[--limit <n>] [--asc | --desc] [--after <cursor>] [--before <cursor>] [--since <ms>] [--until <ms>] [--tag <hex>]"

// validateTag checks a tag's form: 1 or more bytes as pairs of hex digits.
// The server closes the connection on a malformed tag in a CHUNKPUT or XPUT
// header; checkTag checks the length against HELLO.
func validateTag(tag string) error {
	if _, err := hex.DecodeString(tag); err != nil || tag == "" {
		return errors.New("a tag is 1 or more bytes written as pairs of hex digits")
	}
	return nil
}

// validateCursor checks a history cursor: <revision> or
// <revision>:<block_index>.
func validateCursor(cursor string) error {
	revision, block, hasBlock := strings.Cut(cursor, ":")
	_, err := strconv.ParseUint(revision, 10, 64)
	if err == nil && hasBlock {
		_, err = strconv.ParseUint(block, 10, 32)
	}
	if err != nil {
		return errors.New("a cursor is <revision> or <revision>:<block_index>")
	}
	return nil
}

func tagFlag(fs *flag.FlagSet, dst *string, usage string) {
	fs.Func("tag", usage, func(value string) error {
		if err := validateTag(value); err != nil {
			return err
		}
		*dst = value
		return nil
	})
}

// uintFlag registers a flag whose value is an unsigned 64-bit integer.
func uintFlag(fs *flag.FlagSet, name string, usage string, dst *string) {
	fs.Func(name, usage, func(value string) error {
		if _, err := strconv.ParseUint(value, 10, 64); err != nil {
			return errors.New("not an unsigned 64-bit integer")
		}
		*dst = value
		return nil
	})
}

// pastPoint is the point a read looks at: --at <revision> or --at-time <ms>.
type pastPoint struct {
	revision string
	timeMs   string
}

func (p *pastPoint) register(fs *flag.FlagSet) {
	uintFlag(fs, "at", "read as of this revision", &p.revision)
	uintFlag(fs, "at-time", "read as of this commit time (ms since the epoch)", &p.timeMs)
}

func (p pastPoint) check() error {
	if p.revision != "" && p.timeMs != "" {
		return errors.New("--at and --at-time exclude each other")
	}
	return nil
}

// clause is the AT clause that ends the request, empty for the present.
func (p pastPoint) clause() string {
	switch {
	case p.revision != "":
		return " AT " + p.revision
	case p.timeMs != "":
		return " AT TIME " + p.timeMs
	}
	return ""
}

// historyCap checks HELLO's history capability: a server without it refuses
// TAG, AT and the listings, and closes the connection on a TAG in a CHUNKPUT
// or XPUT header.
func (s *session) historyCap() error {
	if !s.history {
		return errors.New("the server does not support block history (its HELLO reply has no history capability)")
	}
	return nil
}

// checkTag checks a tag against HELLO's max_tag_bytes, the longest tag any
// table takes; the table's own history_max_tag_bytes is the server's to check.
func (s *session) checkTag(tag string) error {
	if tag == "" {
		return nil
	}
	if err := s.historyCap(); err != nil {
		return err
	}
	if len(tag)/2 > s.maxTagBytes {
		return fmt.Errorf("a tag of %d bytes exceeds the server's longest tag (max_tag_bytes %d)", len(tag)/2, s.maxTagBytes)
	}
	return nil
}

func (s *session) checkPastPoint(p pastPoint) error {
	if p.clause() == "" {
		return nil
	}
	return s.historyCap()
}

// historyArgs are the history flags of a command in tagCommands or
// pastCommands and the arguments after them.
type historyArgs struct {
	tag  string
	at   pastPoint
	args []string
}

func parseHistoryArgs(cmd string, cmdArgs []string, stderr io.Writer) (historyArgs, error) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var h historyArgs
	var valueFlags []string
	if tagCommands[cmd] {
		tagFlag(fs, &h.tag, "tag the write's history events")
		valueFlags = append(valueFlags, "tag")
	}
	if pastCommands[cmd] {
		h.at.register(fs)
		valueFlags = append(valueFlags, "at", "at-time")
	}
	if err := fs.Parse(protectNegativeArgs(cmdArgs, valueFlags...)); err != nil {
		return historyArgs{}, err
	}
	if err := h.at.check(); err != nil {
		return historyArgs{}, err
	}
	h.args = fs.Args()
	return h, nil
}

// check checks the flags against the server's HELLO before sending.
func (h historyArgs) check(s *session) error {
	if err := s.checkTag(h.tag); err != nil {
		return err
	}
	return s.checkPastPoint(h.at)
}

// tagClause is " TAG <hex>", or empty without a tag.
func tagClause(tag string) string {
	if tag == "" {
		return ""
	}
	return " TAG " + tag
}

type historyListRequest struct {
	cmd                              string
	coords                           []string
	limit                            string
	asc, desc                        bool
	after, before, since, until, tag string
}

func parseHistoryListArgs(cmd string, cmdArgs []string, stderr io.Writer) (historyListRequest, error) {
	coords := historyListCoords[cmd]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	req := historyListRequest{cmd: cmd}
	fs.Func("limit", "list at most this many events (default 100)", func(value string) error {
		if n, err := strconv.ParseUint(value, 10, 64); err != nil || n == 0 {
			return errors.New("not a positive integer")
		}
		req.limit = value
		return nil
	})
	fs.BoolVar(&req.asc, "asc", false, "oldest first")
	fs.BoolVar(&req.desc, "desc", false, "newest first (the default)")
	for _, cursor := range []struct {
		name string
		dst  *string
	}{{"after", &req.after}, {"before", &req.before}} {
		fs.Func(cursor.name, "list the events "+cursor.name+" this cursor", func(value string) error {
			if err := validateCursor(value); err != nil {
				return err
			}
			*cursor.dst = value
			return nil
		})
	}
	uintFlag(fs, "since", "list the events committed at or after this time (ms)", &req.since)
	uintFlag(fs, "until", "list the events committed at or before this time (ms)", &req.until)
	tagFlag(fs, &req.tag, "list the events of writes with this tag")
	if err := fs.Parse(protectNegativeArgs(cmdArgs, "limit", "after", "before", "since", "until", "tag")); err != nil {
		return historyListRequest{}, err
	}
	remaining := fs.Args()
	if len(remaining) != len(coords) {
		return historyListRequest{}, fmt.Errorf("usage: %s %s <%s>", cmd, historyListOptions, strings.Join(coords, "> <"))
	}
	if err := intArgsPrefix(remaining, coords...); err != nil {
		return historyListRequest{}, err
	}
	if req.asc && req.desc {
		return historyListRequest{}, errors.New("--asc and --desc exclude each other")
	}
	req.coords = remaining
	return req, nil
}

// line is the request: HISTORY, CHUNKHISTORY or RANGEHISTORY with the given
// options.
func (r historyListRequest) line() string {
	parts := append([]string{strings.ToUpper(r.cmd)}, r.coords...)
	if r.limit != "" {
		parts = append(parts, "LIMIT", r.limit)
	}
	if r.asc {
		parts = append(parts, "ASC")
	}
	if r.desc {
		parts = append(parts, "DESC")
	}
	for _, option := range []struct{ name, value string }{
		{"AFTER", r.after}, {"BEFORE", r.before}, {"SINCE", r.since}, {"UNTIL", r.until}, {"TAG", r.tag},
	} {
		if option.value != "" {
			parts = append(parts, option.name, option.value)
		}
	}
	return strings.Join(parts, " ")
}

// historyEvent is one event of a history page, as the server's text.
type historyEvent struct {
	revision, timeMs, x, y, before, after, beforeExtra, afterExtra, tag string
}

func (e historyEvent) String() string {
	return fmt.Sprintf("revision=%s time_ms=%s x=%s y=%s before=%s after=%s before_extra=%s after_extra=%s tag=%s",
		e.revision, e.timeMs, e.x, e.y, e.before, e.after, e.beforeExtra, e.afterExtra, e.tag)
}

type historyPage struct {
	// cursor is the next page's AFTER (ascending) or BEFORE (descending),
	// empty when the window is done (END).
	cursor string
	events []historyEvent
}

// parseHistoryPage checks a HISTORY, CHUNKHISTORY or RANGEHISTORY reply:
// END or CURSOR <cursor>, then one item per event:
//
//	<revision> <time_ms> <x> <y> <before> <after> <before_extra> <after_extra> <tag>
//
// x and y are kept as the server's decimal text: at the edge of the chunk
// coordinate range they lie beyond the 64-bit range.
func parseHistoryPage(items [][]byte, blockBits int) (historyPage, error) {
	if len(items) == 0 {
		return historyPage{}, errors.New("empty reply: no END or CURSOR item")
	}
	var page historyPage
	head := string(items[0])
	if cursor, ok := strings.CutPrefix(head, "CURSOR "); ok && validateCursor(cursor) == nil {
		page.cursor = cursor
	} else if head != "END" {
		return historyPage{}, fmt.Errorf("first item %q is not END or CURSOR <cursor>", head)
	}
	for _, item := range items[1:] {
		event, err := parseHistoryEvent(string(item), blockBits)
		if err != nil {
			return historyPage{}, err
		}
		page.events = append(page.events, event)
	}
	return page, nil
}

func parseHistoryEvent(item string, blockBits int) (historyEvent, error) {
	fields := strings.Split(item, " ")
	if len(fields) != 9 {
		return historyEvent{}, fmt.Errorf("event %q has %d fields, not 9", item, len(fields))
	}
	e := historyEvent{fields[0], fields[1], fields[2], fields[3], fields[4], fields[5], fields[6], fields[7], fields[8]}
	_, revisionErr := strconv.ParseUint(e.revision, 10, 64)
	_, timeErr := strconv.ParseUint(e.timeMs, 10, 64)
	valid := revisionErr == nil && timeErr == nil && integerText(e.x) && integerText(e.y) &&
		blockText(e.before, blockBits) && blockText(e.after, blockBits) &&
		extraText(e.beforeExtra) && extraText(e.afterExtra) && (e.tag == "-" || validateTag(e.tag) == nil)
	if !valid {
		return historyEvent{}, fmt.Errorf("malformed event %q", item)
	}
	return e, nil
}

// integerText reports whether text is a decimal integer of any size: an
// optional '-' and digits.
func integerText(text string) bool {
	digits := strings.TrimPrefix(text, "-")
	return digits != "" && strings.Trim(digits, "0123456789") == ""
}

// blockText reports whether text is a block value as GET returns it, or "-"
// for an absent block.
func blockText(text string, blockBits int) bool {
	return text == "-" || (len(text) == blockBits && validateBits(text) == nil)
}

// extraText reports whether text is extra data as <bit_length>:<hex>, or "-"
// for none.
func extraText(text string) bool {
	if text == "-" {
		return true
	}
	bits, value, ok := strings.Cut(text, ":")
	n, err := strconv.ParseUint(bits, 10, 32)
	if !ok || err != nil || n == 0 || uint64(len(value)) != 2*((n+7)/8) {
		return false
	}
	_, err = hex.DecodeString(value)
	return err == nil
}

// runHistory lists a page of block history: one line per event, then END
// when the window is done or CURSOR <cursor> for the next page.
func runHistory(s *session, cmd string, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	req, err := parseHistoryListArgs(cmd, cmdArgs, stderr)
	if err != nil {
		return err
	}
	g, err := s.requireGeometry()
	if err != nil {
		return err
	}
	if err := s.historyCap(); err != nil {
		return err
	}
	if req.limit != "" {
		if limit, _ := strconv.ParseUint(req.limit, 10, 64); limit > uint64(s.maxHistoryLimit) {
			return fmt.Errorf("a limit of %d exceeds the server's max_history_limit (%d)", limit, s.maxHistoryLimit)
		}
	}
	if err := s.checkTag(req.tag); err != nil {
		return err
	}
	items, err := runArray(s.client, req.line())
	if err != nil {
		return err
	}
	page, err := parseHistoryPage(items, g.blockBits)
	if err != nil {
		return fmt.Errorf("%s failed: %w", cmd, err)
	}
	for _, event := range page.events {
		fmt.Fprintln(stdout, event)
	}
	if page.cursor == "" {
		fmt.Fprintln(stdout, "END")
	} else {
		fmt.Fprintf(stdout, "CURSOR %s\n", page.cursor)
	}
	return nil
}
