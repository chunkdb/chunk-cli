package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIScaleReturningAndReadOnlyTransactions(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE terrain (h u8, kind u8 NULL) CHUNK 4 x 4")
	for _, statement := range []string{"SET BLOCK 0 0 IN terrain h = 3", "SET BLOCK 1 1 IN terrain h = 7", "SET BLOCK 2 0 IN terrain h = 5"} {
		s.ok(t, statement)
	}

	text := s.ok(t, "--blocks", "GET AREA 0 0 TO 0 0 FROM terrain COLUMNS h SCALE 2 USING MAX")
	if !strings.Contains(text, "scale = 2 USING MAX\n") || !strings.Contains(text, "present = 2 of 4 blocks\n") {
		t.Fatalf("%q", text)
	}
	json := s.ok(t, "--json", "--blocks", "GET AREA 0 0 TO 0 0 FROM terrain SCALE 4 USING MIN")
	if !strings.Contains(json, `"scale":4,"reduction":"MIN"`) || !strings.Contains(json, `"block_count":1`) {
		t.Fatalf("%q", json)
	}

	if got := s.ok(t, "SET BLOCK 0 0 IN terrain h = h + 1 RETURNING OLD h"); got != "applied = true\nversion = 4\nrow 0 0\n  h = 3\n" {
		t.Fatalf("%q", got)
	}
	if got := s.ok(t, "--json", "SET BLOCK 0 0 IN terrain h = 9 IF h > 100 RETURNING *"); !strings.HasPrefix(got, `{"applied":false,"version":4,"rows":[]}`) {
		t.Fatalf("%q", got)
	}
	if got := s.ok(t, "--json", "DELETE BLOCK 2 0 FROM terrain RETURNING h, kind"); got != `{"applied":true,"version":5,"rows":[{"x":2,"y":0,"h":5,"kind":null}]}`+"\n" {
		t.Fatalf("%q", got)
	}

	file := filepath.Join(t.TempDir(), "read.cql")
	if err := os.WriteFile(file, []byte("BEGIN READ ONLY\nGET BLOCK 0 0 FROM terrain COLUMNS h\nCOMMIT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if got := s.ok(t, "transaction", file); !strings.Contains(got, "h = 4\n") {
		t.Fatalf("%q", got)
	}
	if err := os.WriteFile(file, []byte("BEGIN READ ONLY\nSET BLOCK 0 0 IN terrain h = 1\nCOMMIT\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _, _ := s.cli(t, "", "transaction", file); code == 0 {
		t.Fatal("a read-only transaction wrote")
	}
}
