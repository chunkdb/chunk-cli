package main

import (
	"bytes"
	"errors"
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

func TestParseGlobalFlagsDefaults(t *testing.T) {
	opts, args, err := parseGlobalFlags([]string{"PING"}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.URI != "chunk://127.0.0.1:4242/" || opts.Timeout != 5*time.Second || opts.Statement != (statementOptions{}) {
		t.Fatalf("unexpected defaults: %+v", opts)
	}
	if len(args) != 1 || args[0] != "PING" {
		t.Fatalf("unexpected remaining args: %#v", args)
	}
}

func TestParseGlobalFlagsCustomValues(t *testing.T) {
	opts, args, err := parseGlobalFlags([]string{
		"--uri", "chunks://bot@example.com:9999/",
		"--user", "admin",
		"--password-file", "pw.txt",
		"--new-password-file", "new.txt",
		"--timeout", "3s",
		"--tls-insecure",
		"--tls-server-name", "example.com",
		"--json", "--blocks", "--in", "a.bin", "--out", "b.bin",
		"GET", "BLOCK", "-1", "2", "FROM", "world",
	}, io.Discard)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.URI != "chunks://bot@example.com:9999/" || opts.User != "admin" || opts.PasswordFile != "pw.txt" || opts.Timeout != 3*time.Second ||
		!opts.TLSInsecure || opts.TLSServerName != "example.com" {
		t.Fatalf("unexpected options: %+v", opts)
	}
	if opts.Statement != (statementOptions{json: true, blocks: true, in: "a.bin", out: "b.bin", newPasswordFile: "new.txt"}) {
		t.Fatalf("unexpected statement options: %+v", opts.Statement)
	}
	if strings.Join(args, " ") != "GET BLOCK -1 2 FROM world" {
		t.Fatalf("unexpected remaining args: %#v", args)
	}
}

func TestParseGlobalFlagsErrors(t *testing.T) {
	if _, _, err := parseGlobalFlags([]string{"--help"}, io.Discard); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
	// Statements name their table; --table is gone.
	if _, _, err := parseGlobalFlags([]string{"--table", "x", "PING"}, io.Discard); err == nil {
		t.Fatal("expected an error for --table")
	}
	// Users replace the token.
	if _, _, err := parseGlobalFlags([]string{"--token", "x", "PING"}, io.Discard); err == nil {
		t.Fatal("expected an error for --token")
	}
}

func TestRunLocalCommands(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		code       int
		out, error string
	}{
		{[]string{"version"}, 0, version + "\n", ""},
		{[]string{"help"}, 0, "Usage:", ""},
		{[]string{"--help"}, 0, "Usage:", ""},
		{nil, 1, "", "Usage:"},
		{[]string{"--uri", "http://x/", "PING"}, 1, "", "error: unsupported scheme"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(tc.args, strings.NewReader(""), &stdout, &stderr)
		if code != tc.code || !strings.Contains(stdout.String(), tc.out) || !strings.Contains(stderr.String(), tc.error) {
			t.Errorf("%v: got %d, %q, %q", tc.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]statementInfo{
		"GET BLOCK 10 4 FROM world":                        {kind: statementGetBlock, table: "world"},
		"get block -1 4 from world columns id, name":       {kind: statementGetBlock, table: "world", columns: []string{"id", "name"}},
		"GET CHUNK -2 3 FROM world COLUMNS a":              {kind: statementGetChunk, table: "world", columns: []string{"a"}, cx: -2, cy: 3},
		"GET AREA 0 0 TO 1 1 FROM w":                       {kind: statementGetArea, table: "w"},
		"GET AREA AROUND 0 0 RADIUS 2 FROM w COLUMNS a,b":  {kind: statementGetArea, table: "w", columns: []string{"a", "b"}},
		"DESCRIBE world":                                   {kind: statementDescribe, table: "world"},
		"SET BLOCK 0 0 IN world name = 'GET BLOCK FROM x'": {},
		"GET CHUNK x 0 FROM world":                         {},
		"GET BLOCK 0 0 FROM":                               {},
		"GET BLOCK 0 0 FROM w LIMIT":                       {},
		"SHOW TABLES":                                      {},
		"PING":                                             {},
		"":                                                 {},
		"DESCRIBE":                                         {},
		"GET BLOCK 0 0 FROM w COLUMNS":                     {},
		"GET SOMETHING 0 0 FROM w":                         {},
	}
	for statement, want := range cases {
		if got := classify(statement); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: got %+v, want %+v", statement, got, want)
		}
	}
}

func TestParseLineOptions(t *testing.T) {
	defaults := statementOptions{json: true}
	opts, statement, err := parseLineOptions("--blocks --out a.bin  GET CHUNK 0 0 FROM w  ", defaults)
	if err != nil || statement != "GET CHUNK 0 0 FROM w" || opts != (statementOptions{json: true, blocks: true, out: "a.bin"}) {
		t.Fatalf("got %+v, %q, %v", opts, statement, err)
	}
	opts, statement, err = parseLineOptions("--in c.bin SET CHUNK 1 0 IN w $1", statementOptions{})
	if err != nil || statement != "SET CHUNK 1 0 IN w $1" || opts.in != "c.bin" {
		t.Fatalf("got %+v, %q, %v", opts, statement, err)
	}
	opts, statement, err = parseLineOptions("SET BLOCK 0 0 IN w name = '--json  x'", defaults)
	if err != nil || statement != "SET BLOCK 0 0 IN w name = '--json  x'" || opts != defaults {
		t.Fatalf("got %+v, %q, %v", opts, statement, err)
	}
	opts, statement, err = parseLineOptions("--new-password-file pw.txt ALTER USER bot PASSWORD", statementOptions{})
	if err != nil || statement != "ALTER USER bot PASSWORD" || opts.newPasswordFile != "pw.txt" {
		t.Fatalf("got %+v, %q, %v", opts, statement, err)
	}
	for _, line := range []string{"--json", "--in", "--out ", "--new-password-file", "--bogus PING"} {
		if _, _, err := parseLineOptions(line, defaults); err == nil {
			t.Errorf("%q: expected an error", line)
		}
	}
}

func TestFormatValue(t *testing.T) {
	typ := func(text string) *chunkclient.ColumnType {
		parsed, err := chunkclient.ParseColumnType(text)
		if err != nil {
			t.Fatal(err)
		}
		return &parsed
	}
	bulk := func(s string) chunkclient.Value {
		return chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte(s)}
	}
	for _, tc := range []struct {
		value chunkclient.Value
		typ   *chunkclient.ColumnType
		want  string
	}{
		{chunkclient.Value{Kind: chunkclient.KindNull}, typ("u8"), "NULL"},
		{chunkclient.Value{Kind: chunkclient.KindBool, Bool: true}, typ("bool"), "true"},
		{chunkclient.Value{Kind: chunkclient.KindInteger, Text: "18446744073709551615"}, typ("u64"), "18446744073709551615"},
		{chunkclient.Value{Kind: chunkclient.KindDouble, Text: "-inf"}, typ("f64"), "-inf"},
		{bulk("it's"), typ("text(8)"), "'it''s'"},
		{bulk("a\nb"), typ("text(8)"), `"a\nb"`},
		{bulk("\x00\r\n"), typ("bytes(8)"), "x'000d0a'"},
		{bulk(""), typ("bytes(8)"), "x''"},
		{bulk("\x05"), typ("bits(5)"), "b'10100'"},
		{bulk("default"), nil, "default"},
		{bulk("\xff"), nil, "x'ff'"},
		{chunkclient.Value{Kind: chunkclient.KindSimple, Text: "OK"}, nil, "OK"},
	} {
		if got := formatValue(tc.value, tc.typ); got != tc.want {
			t.Errorf("formatValue(%+v) = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func TestPrintReply(t *testing.T) {
	scan := chunkclient.Value{Kind: chunkclient.KindMap, Map: []chunkclient.MapEntry{
		{Key: chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte("chunks")}, Value: chunkclient.Value{Kind: chunkclient.KindArray, Items: []chunkclient.Value{
			{Kind: chunkclient.KindArray, Items: []chunkclient.Value{{Kind: chunkclient.KindInteger, Text: "0"}, {Kind: chunkclient.KindInteger, Text: "-1"}}},
		}}},
		{Key: chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte("more")}, Value: chunkclient.Value{Kind: chunkclient.KindBool}},
	}}
	var out bytes.Buffer
	if err := printReply(&out, scan, statementOptions{}); err != nil {
		t.Fatal(err)
	}
	if want := "chunks:\n  1) [0, -1]\nmore = false\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := printReply(&out, scan, statementOptions{json: true}); err != nil {
		t.Fatal(err)
	}
	if want := `{"chunks":[[0,-1]],"more":false}` + "\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	out.Reset()
	if err := printReply(&out, chunkclient.Value{Kind: chunkclient.KindArray}, statementOptions{}); err != nil || out.String() != "(empty)\n" {
		t.Fatalf("got %q, %v", out.String(), err)
	}
}
