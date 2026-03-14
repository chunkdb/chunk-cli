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
		{name: "ping extra", cmd: "ping", args: []string{"x"}, wantErr: true},
		{name: "get ok", cmd: "get", args: []string{"1", "2"}, wantErr: false},
		{name: "get bad int", cmd: "get", args: []string{"a", "2"}, wantErr: true},
		{name: "set ok", cmd: "set", args: []string{"1", "2", "0101"}, wantErr: false},
		{name: "set bad bits", cmd: "set", args: []string{"1", "2", "01x1"}, wantErr: true},
		{name: "chunk ok", cmd: "chunk", args: []string{"0", "0"}, wantErr: false},
		{name: "auth too many", cmd: "auth", args: []string{"a", "b"}, wantErr: true},
		{name: "chunkbin passthrough", cmd: "chunkbin", args: []string{"--out", "x", "1", "2"}, wantErr: false},
		{name: "shell ok", cmd: "shell", args: nil, wantErr: false},
		{name: "shell extra", cmd: "shell", args: []string{"ping"}, wantErr: true},
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
