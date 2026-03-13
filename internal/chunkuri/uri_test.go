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
