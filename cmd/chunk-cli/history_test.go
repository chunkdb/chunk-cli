package main

import (
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestValidateTag(t *testing.T) {
	for _, tag := range []string{"00", "6a6f62", "ABcd", strings.Repeat("ff", 255)} {
		if err := validateTag(tag); err != nil {
			t.Fatalf("validateTag(%q): %v", tag, err)
		}
	}
	for _, tag := range []string{"", "a", "abc", "zz", "0g", "-1", "6a 6f"} {
		if err := validateTag(tag); err == nil {
			t.Fatalf("validateTag(%q): expected an error", tag)
		}
	}
}

func TestValidateCursor(t *testing.T) {
	for _, cursor := range []string{"0", "1041", "12:74", "18446744073709551615:4294967295"} {
		if err := validateCursor(cursor); err != nil {
			t.Fatalf("validateCursor(%q): %v", cursor, err)
		}
	}
	for _, cursor := range []string{"", ":", "1:", ":1", "-1", "+1", "1:-1", "1:4294967296", "18446744073709551616", "1:2:3", "a"} {
		if err := validateCursor(cursor); err == nil {
			t.Fatalf("validateCursor(%q): expected an error", cursor)
		}
	}
}

func TestHistoryListRequestLine(t *testing.T) {
	cases := []struct {
		cmd  string
		args []string
		want string
	}{
		{"history", []string{"10", "4"}, "HISTORY 10 4"},
		{"history", []string{"--limit", "20", "--tag", "6a6f62", "-10", "-4"}, "HISTORY -10 -4 LIMIT 20 TAG 6a6f62"},
		{"chunkhistory", []string{"--asc", "--after", "1041", "0", "0"}, "CHUNKHISTORY 0 0 ASC AFTER 1041"},
		{"chunkhistory", []string{"--desc", "--before", "9:3", "--since", "5", "--until", "7", "-1", "2"},
			"CHUNKHISTORY -1 2 DESC BEFORE 9:3 SINCE 5 UNTIL 7"},
		{"rangehistory", []string{"--after", "1", "--before", "9", "-1", "-1", "1", "1"}, "RANGEHISTORY -1 -1 1 1 AFTER 1 BEFORE 9"},
	}
	for _, tc := range cases {
		req, err := parseHistoryListArgs(tc.cmd, tc.args, io.Discard)
		if err != nil {
			t.Fatalf("%s %q: %v", tc.cmd, tc.args, err)
		}
		if got := req.line(); got != tc.want {
			t.Fatalf("%s %q: got %q, want %q", tc.cmd, tc.args, got, tc.want)
		}
	}
}

func TestParseHistoryPage(t *testing.T) {
	items := func(texts ...string) [][]byte {
		out := make([][]byte, len(texts))
		for i, text := range texts {
			out[i] = []byte(text)
		}
		return out
	}
	page, err := parseHistoryPage(items("END",
		"7 1791377299721 10 4 0001 0001 3:05 - 0b",
		"6 1791377299720 -10 4 - 0001 - 12:0d08 -",
		"5 0 18446744073709551615 -18446744073709551616 1111 - - - "+strings.Repeat("ff", 255)), 4)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := historyPage{events: []historyEvent{
		{"7", "1791377299721", "10", "4", "0001", "0001", "3:05", "-", "0b"},
		{"6", "1791377299720", "-10", "4", "-", "0001", "-", "12:0d08", "-"},
		{"5", "0", "18446744073709551615", "-18446744073709551616", "1111", "-", "-", "-", strings.Repeat("ff", 255)},
	}}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("got %+v, want %+v", page, want)
	}
	if got := page.events[0].String(); got != "revision=7 time_ms=1791377299721 x=10 y=4 before=0001 after=0001 before_extra=3:05 after_extra=- tag=0b" {
		t.Fatalf("event line %q", got)
	}
	// A page can be empty and still carry a cursor; END alone ends the window.
	for head, cursor := range map[string]string{"CURSOR 9": "9", "CURSOR 2:74": "2:74", "END": ""} {
		page, err := parseHistoryPage(items(head), 4)
		if err != nil || page.cursor != cursor || len(page.events) != 0 {
			t.Fatalf("%q: got %+v, %v", head, page, err)
		}
	}

	malformed := map[string][][]byte{
		"empty reply":         nil,
		"no head":             items("1 5 0 0 - 0001 - - -"),
		"null head":           {nil},
		"lowercase end":       items("end"),
		"cursor without one":  items("CURSOR"),
		"bad cursor":          items("CURSOR 1:x"),
		"cursor extra field":  items("CURSOR 1 2"),
		"null event":          {[]byte("END"), nil},
		"eight fields":        items("END", "1 5 0 0 - 0001 - -"),
		"ten fields":          items("END", "1 5 0 0 - 0001 - - - -"),
		"double space":        items("END", "1 5 0 0  - 0001 - - -"),
		"bad revision":        items("END", "-1 5 0 0 - 0001 - - -"),
		"bad time":            items("END", "1 x 0 0 - 0001 - - -"),
		"bad x":               items("END", "1 5 +0 0 - 0001 - - -"),
		"bad y":               items("END", "1 5 0 - - 0001 - - -"),
		"short bits":          items("END", "1 5 0 0 - 001 - - -"),
		"long bits":           items("END", "1 5 0 0 00001 0001 - - -"),
		"bad bits":            items("END", "1 5 0 0 0002 0001 - - -"),
		"extra without colon": items("END", "1 5 0 0 0001 0001 05 - -"),
		"extra of 0 bits":     items("END", "1 5 0 0 0001 0001 0: - -"),
		"extra too short":     items("END", "1 5 0 0 0001 0001 - 12:0d -"),
		"extra too long":      items("END", "1 5 0 0 0001 0001 - 3:0500 -"),
		"extra not hex":       items("END", "1 5 0 0 0001 0001 - 3:zz -"),
		"odd tag":             items("END", "1 5 0 0 - 0001 - - abc"),
		"empty tag":           items("END", "1 5 0 0 - 0001 - - "),
	}
	for name, reply := range malformed {
		if _, err := parseHistoryPage(reply, 4); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
