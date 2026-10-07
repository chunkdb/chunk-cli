package main

import (
	"flag"
	"testing"
	"time"
)

func TestValidateBits(t *testing.T) {
	if err := validateBits("010101"); err != nil {
		t.Fatalf("expected valid bit string, got %v", err)
	}
	if err := validateBits("01a01"); err == nil {
		t.Fatalf("expected error for non-binary bit string")
	}
}

func TestValidateCommandArgs(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		args    []string
		wantErr bool
	}{
		{name: "ping ok", cmd: "ping", args: nil, wantErr: false},
		{name: "tables ok", cmd: "tables", args: nil, wantErr: false},
		{name: "tables extra", cmd: "tables", args: []string{"x"}, wantErr: true},
		{name: "use ok", cmd: "use", args: []string{"terrain"}, wantErr: false},
		{name: "use missing name", cmd: "use", args: nil, wantErr: true},
		{name: "tableinfo ok", cmd: "tableinfo", args: []string{"terrain"}, wantErr: false},
		{name: "tabledrop extra", cmd: "tabledrop", args: []string{"a", "b"}, wantErr: true},
		{name: "tablecreate ok", cmd: "tablecreate", args: []string{"terrain", "block_bits", "4"}, wantErr: false},
		{name: "tablecreate odd pairs", cmd: "tablecreate", args: []string{"terrain", "block_bits"}, wantErr: true},
		{name: "tableset ok", cmd: "tableset", args: []string{"terrain", "checkpoint_updates", "3"}, wantErr: false},
		{name: "tableset no option", cmd: "tableset", args: []string{"terrain"}, wantErr: true},
		{name: "ping extra", cmd: "ping", args: []string{"x"}, wantErr: true},
		{name: "get ok", cmd: "get", args: []string{"1", "2"}, wantErr: false},
		{name: "get bad int", cmd: "get", args: []string{"a", "2"}, wantErr: true},
		{name: "set ok", cmd: "set", args: []string{"1", "2", "0101"}, wantErr: false},
		{name: "set bad bits", cmd: "set", args: []string{"1", "2", "01x1"}, wantErr: true},
		{name: "set empty bits", cmd: "set", args: []string{"1", "2", ""}, wantErr: true},
		{name: "unset ok", cmd: "unset", args: []string{"1", "2"}, wantErr: false},
		{name: "unset bad int", cmd: "unset", args: []string{"1", "b"}, wantErr: true},
		{name: "mset one triple ok", cmd: "mset", args: []string{"1", "2", "0101"}, wantErr: false},
		{name: "mset two triples ok", cmd: "mset", args: []string{"1", "2", "0101", "3", "4", "1010"}, wantErr: false},
		{name: "mset wrong arity", cmd: "mset", args: []string{"1", "2"}, wantErr: true},
		{name: "mset empty", cmd: "mset", args: nil, wantErr: true},
		{name: "mset bad bits", cmd: "mset", args: []string{"1", "2", "01x1"}, wantErr: true},
		{name: "mset bad int", cmd: "mset", args: []string{"a", "2", "0101"}, wantErr: true},
		{name: "mget one pair ok", cmd: "mget", args: []string{"1", "2"}, wantErr: false},
		{name: "mget two pairs ok", cmd: "mget", args: []string{"1", "2", "3", "4"}, wantErr: false},
		{name: "mget wrong arity", cmd: "mget", args: []string{"1", "2", "3"}, wantErr: true},
		{name: "mget empty", cmd: "mget", args: nil, wantErr: true},
		{name: "mget bad int", cmd: "mget", args: []string{"1", "b"}, wantErr: true},
		{name: "chunkexists ok", cmd: "chunkexists", args: []string{"0", "0"}, wantErr: false},
		{name: "chunkexists bad int", cmd: "chunkexists", args: []string{"a", "0"}, wantErr: true},
		{name: "chunkset ok", cmd: "chunkset", args: []string{"0", "0", "0101"}, wantErr: false},
		{name: "chunkset bad bits", cmd: "chunkset", args: []string{"0", "0", "01x1"}, wantErr: true},
		{name: "chunkstate ok", cmd: "chunkstate", args: []string{"0", "0"}, wantErr: false},
		{name: "chunkstate bad int", cmd: "chunkstate", args: []string{"0", "x"}, wantErr: true},
		{name: "chunksetstate ok", cmd: "chunksetstate", args: []string{"0", "0", "0101|1010"}, wantErr: false},
		{name: "chunksetstate missing separator", cmd: "chunksetstate", args: []string{"0", "0", "01011010"}, wantErr: true},
		{name: "chunk ok", cmd: "chunk", args: []string{"0", "0"}, wantErr: false},
		{name: "chunkget ok", cmd: "chunkget", args: []string{"--state", "--zrle", "--out", "x", "1", "2"}, wantErr: false},
		{name: "chunkget bad int", cmd: "chunkget", args: []string{"1", "y"}, wantErr: true},
		{name: "chunkget unknown flag", cmd: "chunkget", args: []string{"--raw", "1", "2"}, wantErr: true},
		{name: "chunkput hex ok", cmd: "chunkput", args: []string{"1", "2", "a0a1"}, wantErr: false},
		{name: "chunkput flags ok", cmd: "chunkput", args: []string{"--state", "--zrle", "--if", "7", "1", "2", "a0a1ff"}, wantErr: false},
		{name: "chunkput file ok", cmd: "chunkput", args: []string{"--in", "x", "1", "2"}, wantErr: false},
		{name: "chunkput bad version", cmd: "chunkput", args: []string{"--if", "-1", "1", "2", "a0"}, wantErr: true},
		{name: "chunkput bad hex", cmd: "chunkput", args: []string{"1", "2", "zz"}, wantErr: true},
		{name: "chunkput bad int", cmd: "chunkput", args: []string{"x", "2", "a0"}, wantErr: true},
		{name: "chunkput missing payload", cmd: "chunkput", args: []string{"1", "2"}, wantErr: true},
		{name: "chunkput in and hex", cmd: "chunkput", args: []string{"--in", "x", "1", "2", "a0"}, wantErr: true},
		{name: "shell ok", cmd: "shell", args: nil, wantErr: false},
		{name: "shell extra", cmd: "shell", args: []string{"ping"}, wantErr: true},
		{name: "chunkscan ok", cmd: "chunkscan", args: []string{"10"}, wantErr: false},
		{name: "chunkscan cursor ok", cmd: "chunkscan", args: []string{"10", "-1", "2"}, wantErr: false},
		{name: "chunkscan bad limit", cmd: "chunkscan", args: []string{"-1"}, wantErr: true},
		{name: "chunkscan wrong arity", cmd: "chunkscan", args: []string{"10", "1"}, wantErr: true},
		{name: "chunkrange ok", cmd: "chunkrange", args: []string{"-1", "-1", "1", "1"}, wantErr: false},
		{name: "chunkrange wrong arity", cmd: "chunkrange", args: []string{"0", "0", "1"}, wantErr: true},
		{name: "chunkradius ok", cmd: "chunkradius", args: []string{"0", "0", "2"}, wantErr: false},
		{name: "chunkradius bad radius", cmd: "chunkradius", args: []string{"0", "0", "-2"}, wantErr: true},
		{name: "chunkver ok", cmd: "chunkver", args: []string{"0", "0"}, wantErr: false},
		{name: "chunkver bad int", cmd: "chunkver", args: []string{"a", "0"}, wantErr: true},
		{name: "chunkbatch set ok", cmd: "chunkbatch", args: []string{"0", "0", "SET", "1", "2", "0101"}, wantErr: false},
		{name: "chunkbatch versioned mixed ok", cmd: "chunkbatch", args: []string{"--if", "7", "0", "0", "SET", "1", "2", "0101", "UNSET", "3", "4"}, wantErr: false},
		{name: "chunkbatch bad version", cmd: "chunkbatch", args: []string{"--if", "x", "0", "0", "UNSET", "1", "2"}, wantErr: true},
		{name: "chunkbatch old placeholder", cmd: "chunkbatch", args: []string{"0", "0", "-", "SET", "1", "2", "0101"}, wantErr: true},
		{name: "chunkbatch bad op", cmd: "chunkbatch", args: []string{"0", "0", "NOPE", "1", "2"}, wantErr: true},
		{name: "chunkbatch truncated set", cmd: "chunkbatch", args: []string{"0", "0", "SET", "1", "2"}, wantErr: true},
		{name: "chunkbatch missing ops", cmd: "chunkbatch", args: []string{"0", "0"}, wantErr: true},
		{name: "chunkbatch extra ops ok", cmd: "chunkbatch", args: []string{"0", "0", "SET", "1", "1", "0101", "XPUT", "1", "1", "1011", "XDEL", "0", "1"}, wantErr: false},
		{name: "chunkbatch xput bad bits", cmd: "chunkbatch", args: []string{"0", "0", "XPUT", "1", "1", "10x1"}, wantErr: true},
		{name: "chunkbatch truncated xput", cmd: "chunkbatch", args: []string{"0", "0", "XPUT", "1", "1"}, wantErr: true},
		{name: "chunkbatch xdel bad int", cmd: "chunkbatch", args: []string{"0", "0", "XDEL", "1", "y"}, wantErr: true},
		{name: "xget ok", cmd: "xget", args: []string{"1", "2"}, wantErr: false},
		{name: "xget negative ok", cmd: "xget", args: []string{"-1", "-2"}, wantErr: false},
		{name: "xget bits ok", cmd: "xget", args: []string{"--bits", "-1", "2"}, wantErr: false},
		{name: "xget bad int", cmd: "xget", args: []string{"1", "y"}, wantErr: true},
		{name: "xget unknown flag", cmd: "xget", args: []string{"--hex", "1", "2"}, wantErr: true},
		{name: "xget wrong arity", cmd: "xget", args: []string{"1"}, wantErr: true},
		{name: "xput bits ok", cmd: "xput", args: []string{"-1", "2", "101"}, wantErr: false},
		{name: "xput bad bits", cmd: "xput", args: []string{"1", "2", "10x"}, wantErr: true},
		{name: "xput empty bits", cmd: "xput", args: []string{"1", "2", ""}, wantErr: true},
		{name: "xput bits with bit length", cmd: "xput", args: []string{"--bit-length", "3", "1", "2", "101"}, wantErr: true},
		{name: "xput hex ok", cmd: "xput", args: []string{"--hex", "--bit-length", "12", "1", "2", "0d08"}, wantErr: false},
		{name: "xput hex without bit length", cmd: "xput", args: []string{"--hex", "1", "2", "0d08"}, wantErr: true},
		{name: "xput hex size mismatch", cmd: "xput", args: []string{"--hex", "--bit-length", "8", "1", "2", "0d08"}, wantErr: true},
		{name: "xput bad hex", cmd: "xput", args: []string{"--hex", "--bit-length", "8", "1", "2", "zz"}, wantErr: true},
		{name: "xput zero bit length", cmd: "xput", args: []string{"--hex", "--bit-length", "0", "1", "2", "00"}, wantErr: true},
		{name: "xput file ok", cmd: "xput", args: []string{"--bit-length", "9", "--in", "x", "1", "2"}, wantErr: false},
		{name: "xput file and hex", cmd: "xput", args: []string{"--hex", "--bit-length", "9", "--in", "x", "1", "2"}, wantErr: true},
		{name: "xput file with value", cmd: "xput", args: []string{"--bit-length", "9", "--in", "x", "1", "2", "0d08"}, wantErr: true},
		{name: "xput missing value", cmd: "xput", args: []string{"1", "2"}, wantErr: true},
		{name: "xdel ok", cmd: "xdel", args: []string{"-1", "2"}, wantErr: false},
		{name: "xdel bad int", cmd: "xdel", args: []string{"1", "y"}, wantErr: true},
		{name: "chunkget extra ok", cmd: "chunkget", args: []string{"--extra", "--zrle", "1", "2"}, wantErr: false},
		{name: "chunkput extra ok", cmd: "chunkput", args: []string{"--extra", "--in", "x", "1", "2"}, wantErr: false},
		{name: "walflush ok", cmd: "walflush", args: nil, wantErr: false},
		{name: "walflush extra", cmd: "walflush", args: []string{"x"}, wantErr: true},
		{name: "metrics ok", cmd: "metrics", args: nil, wantErr: false},
		{name: "metrics extra", cmd: "metrics", args: []string{"x"}, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCommandArgs(tc.cmd, tc.args)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestParseGlobalFlagsDefaults(t *testing.T) {
	opts, args, err := parseGlobalFlags([]string{"ping"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.URI != "chunk://127.0.0.1:4242/" {
		t.Fatalf("unexpected default uri: %q", opts.URI)
	}
	if opts.Timeout != 5*time.Second {
		t.Fatalf("unexpected default timeout: %v", opts.Timeout)
	}
	if len(args) != 1 || args[0] != "ping" {
		t.Fatalf("unexpected remaining args: %#v", args)
	}
}

func TestParseGlobalFlagsCustomValues(t *testing.T) {
	opts, args, err := parseGlobalFlags([]string{
		"--uri", "chunks://token@example.com:9999/",
		"--token", "override",
		"--timeout", "3s",
		"--tls-insecure",
		"--tls-server-name", "example.com",
		"--table", "terrain",
		"get", "1", "2",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if opts.URI != "chunks://token@example.com:9999/" {
		t.Fatalf("unexpected uri: %q", opts.URI)
	}
	if opts.TokenOverride != "override" {
		t.Fatalf("unexpected token: %q", opts.TokenOverride)
	}
	if opts.Timeout != 3*time.Second {
		t.Fatalf("unexpected timeout: %v", opts.Timeout)
	}
	if !opts.TLSInsecure {
		t.Fatalf("expected tls-insecure to be true")
	}
	if opts.TLSServerName != "example.com" {
		t.Fatalf("unexpected tls server name: %q", opts.TLSServerName)
	}
	if opts.Table != "terrain" {
		t.Fatalf("unexpected table: %q", opts.Table)
	}
	if len(args) != 3 || args[0] != "get" || args[1] != "1" || args[2] != "2" {
		t.Fatalf("unexpected remaining args: %#v", args)
	}
}

func TestParseGlobalFlagsHelp(t *testing.T) {
	_, _, err := parseGlobalFlags([]string{"--help"})
	if err == nil {
		t.Fatalf("expected help error")
	}
	if err != flag.ErrHelp {
		t.Fatalf("expected flag.ErrHelp, got %v", err)
	}
}

func TestCommandVerb(t *testing.T) {
	if got := commandVerb("GET 10 12"); got != "get" {
		t.Fatalf("unexpected verb: %q", got)
	}
	if got := commandVerb(""); got != "command" {
		t.Fatalf("unexpected fallback verb: %q", got)
	}
}
