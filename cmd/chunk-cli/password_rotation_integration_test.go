package main

import (
	"strings"
	"testing"
)

func TestCLIPasswordRotation(t *testing.T) {
	s := startServerWith(t, false, true)
	first := writePasswordFile(t, "first-pass")
	for range 2 {
		if out := s.ok(t, "--new-password-file", first, "CREATE USER IF NOT EXISTS bot PASSWORD"); out != "OK\n" {
			t.Fatalf("CREATE USER IF NOT EXISTS: %q", out)
		}
	}
	second := writePasswordFile(t, "second-pass")
	s.fails(t, "ADD PASSWORD reuses the salt", "--new-password-file", second, "ALTER USER bot ADD PASSWORD")
	if code, out, errOut := s.cliAt(t, s.uriFor("bot", "first-pass"), "", "--new-password-file", second, "ALTER USER bot ADD PASSWORD"); code != 0 || out != "OK\n" {
		t.Fatalf("ADD PASSWORD: %d %q %q", code, out, errOut)
	}
	for _, password := range []string{"first-pass", "second-pass"} {
		if code, out, errOut := s.cliAt(t, s.uriFor("bot", password), "", "PING"); code != 0 || out != "PONG\n" {
			t.Fatalf("login with %s: %d %q %q", password, code, out, errOut)
		}
	}
	if code, out, errOut := s.cliAt(t, s.uriFor("bot", "second-pass"), "", "ALTER USER bot DROP OLD VERIFIER"); code != 0 || out != "OK\n" {
		t.Fatalf("DROP OLD VERIFIER: %d %q %q", code, out, errOut)
	}
	if code, _, errOut := s.cliAt(t, s.uriFor("bot", "first-pass"), "", "PING"); code == 0 || !strings.Contains(errOut, "AUTH_FAILED") {
		t.Fatalf("the old password still logs in: %d %q", code, errOut)
	}
}
