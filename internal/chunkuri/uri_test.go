package chunkuri

import "testing"

func TestParseChunkURI(t *testing.T) {
	parsed, err := Parse("chunk://token@localhost:4242/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if parsed.Secure {
		t.Fatalf("expected insecure URI")
	}
	if parsed.Token != "token" {
		t.Fatalf("unexpected token: %q", parsed.Token)
	}
	if parsed.Host != "localhost" {
		t.Fatalf("unexpected host: %q", parsed.Host)
	}
	if parsed.Port != 4242 {
		t.Fatalf("unexpected port: %d", parsed.Port)
	}
}

func TestParseChunksDefaultPort(t *testing.T) {
	parsed, err := Parse("chunks://abc@example.com/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !parsed.Secure {
		t.Fatalf("expected secure URI")
	}
	if parsed.Port != DefaultPort {
		t.Fatalf("expected default port %d, got %d", DefaultPort, parsed.Port)
	}
}

func TestParseInvalidScheme(t *testing.T) {
	if _, err := Parse("http://localhost:4242/"); err == nil {
		t.Fatalf("expected error for unsupported scheme")
	}
}

func TestParseTablePath(t *testing.T) {
	for raw, want := range map[string]string{
		"chunk://t@h:1/":        "",
		"chunk://t@h:1":         "",
		"chunk://t@h:1/terrain": "terrain",
	} {
		parsed, err := Parse(raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", raw, err)
		}
		if parsed.Table != want {
			t.Fatalf("Parse(%q).Table = %q, want %q", raw, parsed.Table, want)
		}
	}
	if _, err := Parse("chunk://t@h:1/a/b"); err == nil {
		t.Fatal("expected an error for a two-segment path")
	}
}
