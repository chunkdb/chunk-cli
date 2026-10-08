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
	if parsed.User != "token" || parsed.Password != "" {
		t.Fatalf("unexpected user: %q %q", parsed.User, parsed.Password)
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

func TestParseCredentials(t *testing.T) {
	cases := []struct {
		raw, user, password string
	}{
		{"chunk://h:1/", "", ""},
		{"chunk://bot@h:1/", "bot", ""},
		{"chunk://bot:pw@h:1/", "bot", "pw"},
		{"chunks://bot:p%3Aw%40x%2Fy%25@h:1/", "bot", "p:w@x/y%"},
		{"chunk://b%6Ft:@h:1/", "bot", ""},
	}
	for _, tc := range cases {
		parsed, err := Parse(tc.raw)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.raw, err)
		}
		if parsed.User != tc.user || parsed.Password != tc.password || parsed.Host != "h" {
			t.Fatalf("Parse(%q) = %+v, want user %q, password %q", tc.raw, parsed, tc.user, tc.password)
		}
	}
	for _, raw := range []string{"chunk://:pw@h:1/", "chunk://bot:%zz@h:1/"} {
		if _, err := Parse(raw); err == nil {
			t.Fatalf("Parse(%q): expected an error", raw)
		}
	}
}
