package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestReadPasswordFile(t *testing.T) {
	for content, want := range map[string]string{
		"pw\n":           "pw",
		"pw":             "pw",
		"p w:@/\r\nnext": "p w:@/",
	} {
		if got, err := readPasswordFile(writeFile(t, content)); err != nil || got != want {
			t.Errorf("%q: got %q, %v", content, got, err)
		}
	}
	for _, content := range []string{"", "\n", "\r\nx"} {
		if _, err := readPasswordFile(writeFile(t, content)); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Errorf("%q: got %v", content, err)
		}
	}
	if _, err := readPasswordFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestLoginFor(t *testing.T) {
	t.Setenv(passwordEnv, "")
	file := writeFile(t, "from-file\n")
	notTerminal := console{in: strings.NewReader(""), out: &bytes.Buffer{}}
	parse := func(raw string) chunkuri.Parsed {
		parsed, err := chunkuri.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	cases := []struct {
		opts globalOptions
		uri  string
		env  string
		want chunkclient.Login
	}{
		{globalOptions{}, "chunk://h/", "ignored", chunkclient.Login{}},
		{globalOptions{}, "chunk://bot:p%40ss@h/", "", chunkclient.Login{User: "bot", Password: "p@ss"}},
		{globalOptions{PasswordFile: file}, "chunk://bot:uri@h/", "env", chunkclient.Login{User: "bot", Password: "from-file"}},
		{globalOptions{}, "chunk://bot:uri@h/", "env", chunkclient.Login{User: "bot", Password: "uri"}},
		{globalOptions{}, "chunk://bot@h/", "env", chunkclient.Login{User: "bot", Password: "env"}},
		{globalOptions{User: "admin"}, "chunk://bot:uri@h/", "env", chunkclient.Login{User: "admin", Password: "env"}},
		{globalOptions{User: "admin", PasswordFile: file}, "chunk://h/", "", chunkclient.Login{User: "admin", Password: "from-file"}},
	}
	for _, tc := range cases {
		t.Setenv(passwordEnv, tc.env)
		got, err := loginFor(tc.opts, parse(tc.uri), notTerminal)
		if err != nil || got != tc.want {
			t.Errorf("%+v %s %q: got %+v, %v", tc.opts, tc.uri, tc.env, got, err)
		}
	}

	t.Setenv(passwordEnv, "")
	for _, tc := range []struct {
		opts globalOptions
		uri  string
		want string
	}{
		{globalOptions{}, "chunk://bot@h/", "no password for user bot"},
		{globalOptions{User: "admin"}, "chunk://h/", "no password for user admin"},
		{globalOptions{PasswordFile: file}, "chunk://h/", "--password-file needs a user"},
		{globalOptions{User: "bot", PasswordFile: filepath.Join(t.TempDir(), "missing")}, "chunk://h/", "read the password"},
	} {
		if _, err := loginFor(tc.opts, parse(tc.uri), notTerminal); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v %s: got %v, want %q", tc.opts, tc.uri, err, tc.want)
		}
	}
}

func TestPasswordStatement(t *testing.T) {
	for statement, want := range map[string][2]string{
		"CREATE USER bot PASSWORD":                {"CREATE USER bot VERIFIER $1", "bot"},
		"create  user bot password manages users": {"create user bot VERIFIER $1 manages users", "bot"},
		"ALTER USER bot PASSWORD":                 {"ALTER USER bot VERIFIER $1", "bot"},
	} {
		rewritten, user, ok := passwordStatement(statement)
		if !ok || rewritten != want[0] || user != want[1] {
			t.Errorf("%q: got %q, %q, %v", statement, rewritten, user, ok)
		}
	}
	for _, statement := range []string{
		"CREATE USER bot VERIFIER $1", "ALTER USER bot MANAGES USERS", "CREATE TABLE password (a u8) CHUNK 2 x 2",
		"DROP USER bot", "CREATE USER bot", "GRANT READ ON * TO bot", "",
	} {
		if _, _, ok := passwordStatement(statement); ok {
			t.Errorf("%q was rewritten", statement)
		}
	}
	if _, err := newPassword(statementOptions{}, "bot", console{in: strings.NewReader("pw\n"), out: &bytes.Buffer{}}); err == nil ||
		!strings.Contains(err.Error(), "--new-password-file") {
		t.Fatalf("without a file or terminal: %v", err)
	}
	if got, err := newPassword(statementOptions{newPasswordFile: writeFile(t, "new\n")}, "bot", console{}); err != nil || got != "new" {
		t.Fatalf("from a file: %q, %v", got, err)
	}
}
