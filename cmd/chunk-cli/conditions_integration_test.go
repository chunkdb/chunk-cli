package main

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIConditionsAndWhere(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE wallet (coins u16, kind u8) CHUNK 2 x 2")
	s.ok(t, "SET BLOCK 0 0 IN wallet coins = 5, kind = 1")
	s.ok(t, "SET BLOCK 6 0 IN wallet coins = 1, kind = 4")
	two := filepath.Join(t.TempDir(), "two.bin")
	if err := os.WriteFile(two, binary.LittleEndian.AppendUint64(nil, 2), 0600); err != nil {
		t.Fatal(err)
	}

	s.ok(t, "--in", two, "SET BLOCK 0 0 IN wallet coins = coins + $1 IF kind = 1")
	if got := s.ok(t, "GET BLOCK 0 0 FROM wallet COLUMNS coins"); !strings.Contains(got, "7") {
		t.Fatalf("computed value not written: %q", got)
	}
	s.failsCode(t, 2, "CONDITION_FAILED current=", "SET BLOCK 0 0 IN wallet coins = 0 IF coins > 100")
	s.fails(t, "OUT_OF_RANGE", "SET BLOCK 0 0 IN wallet coins = coins - 8")
	s.failsCode(t, 2, "CONDITION_FAILED", "DELETE BLOCK 6 0 FROM wallet IF kind = 9")

	if got := s.ok(t, "--in", two, "AGGREGATE AREA 0 0 TO 3 0 FROM wallet COUNT(*), SUM(coins) WHERE kind > $1"); got != "count(*) = 1\nsum(coins) = 1\n" {
		t.Fatalf("%q", got)
	}
	if got := s.ok(t, "--json", "SCAN CHUNKS FROM wallet LIMIT 1 WHERE kind > 2"); !strings.Contains(got, `"chunks":[]`) || !strings.Contains(got, `"more":true`) || !strings.Contains(got, `"after":[0,0]`) {
		t.Fatalf("%q", got)
	}
	got := s.ok(t, "--json", "SCAN CHUNKS FROM wallet LIMIT 1 WHERE kind > 2 WITH DATA")
	if !strings.Contains(got, `"chunks":[]`) || !strings.Contains(got, `"more":true,"after":[0,0]`) {
		t.Fatalf("%q", got)
	}
	got = s.ok(t, "SCAN CHUNKS FROM wallet AFTER 0 0 WHERE kind > 2 WITH DATA")
	if !strings.Contains(got, "chunk 3 0\n") || !strings.Contains(got, "after = NULL\n") {
		t.Fatalf("%q", got)
	}
	got = s.ok(t, "--blocks", "SCAN CHUNKS FROM wallet WITH DATA WHERE kind > 2 COLUMNS (kind) AFTER 0 0 LIMIT 1")
	if !strings.Contains(got, "chunk 3 0\n") || !strings.Contains(got, "  projected = true\n") ||
		!strings.Contains(got, "    kind = 4\n") || strings.Contains(got, "coins =") {
		t.Fatalf("scan projection: %q", got)
	}
	s.ok(t, "DELETE BLOCK 6 0 FROM wallet IF kind = 4")
}
