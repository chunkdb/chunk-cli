package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheck(t *testing.T) {
	ok := writeLog(t,
		`{"Action":"run","Test":"TestA"}`,
		`{"Action":"pass","Test":"TestA"}`,
		`{"Action":"pass","Test":"TestB"}`,
		`{"Action":"pass"}`)
	if count, err := check(ok, []string{"TestA", "TestB"}); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := check(ok, []string{"TestA", "TestC"}); err == nil || !strings.Contains(err.Error(), "TestC") {
		t.Fatalf("missing required test not reported: %v", err)
	}
	skipped := writeLog(t, `{"Action":"pass","Test":"TestA"}`, `{"Action":"skip","Test":"TestS"}`)
	if _, err := check(skipped, []string{"TestA"}); err == nil || !strings.Contains(err.Error(), "TestS") {
		t.Fatalf("skipped test not reported: %v", err)
	}
	broken := writeLog(t, `not json`)
	if _, err := check(broken, nil); err == nil {
		t.Fatal("malformed log accepted")
	}
}
