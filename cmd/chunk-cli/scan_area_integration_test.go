package main

import (
	"strings"
	"testing"
)

// TestCLIScanArea lists the chunks of an AREA or AREA AROUND selector.
func TestCLIScanArea(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE field (kind u8) CHUNK 2 x 2")
	// Chunks 0 0, 1 0, 0 2 and 3 3 at versions 1 to 4.
	for _, block := range []string{"0 0", "2 0", "0 4", "6 6"} {
		s.ok(t, "SET BLOCK "+block+" IN field kind = 1")
	}
	if got := s.ok(t, "SCAN CHUNKS FROM field AREA 0 0 TO 0 3"); !strings.HasPrefix(got, "chunks:\n  1) [0, 0, 1]\n  2) [0, 2, 3]\nmore = false\n") {
		t.Fatalf("%q", got)
	}
	got := s.ok(t, "--json", "SCAN CHUNKS FROM field AREA 0 0 TO 1 2 WITH DATA")
	if !strings.Contains(got, `"chunks":[{"cx":0,"cy":0,"version":1,`) || !strings.Contains(got, `{"cx":0,"cy":2,"version":3,`) || !strings.Contains(got, `{"cx":1,"cy":0,"version":2,`) || strings.Contains(got, `"cx":3`) {
		t.Fatalf("%q", got)
	}
	got = s.ok(t, "SCAN CHUNKS FROM field AREA AROUND 0 0 RADIUS 1 WITH DATA")
	if !strings.Contains(got, "chunk 0 0\n") || !strings.Contains(got, "chunk 1 0\n") || strings.Contains(got, "chunk 0 2\n") || !strings.Contains(got, "more = false\n") {
		t.Fatalf("%q", got)
	}
	got = s.ok(t, "--json", "SCAN CHUNKS FROM field AREA 0 1 TO 3 3 WITH DATA AFTER 0 0 WHERE kind > 0 LIMIT 1")
	if !strings.Contains(got, `"chunks":[{"cx":0,"cy":2,"version":3,`) || !strings.Contains(got, `"more":true,"after":[0,2]`) {
		t.Fatalf("%q", got)
	}
	s.fails(t, "INVALID_ARGUMENT", "SCAN CHUNKS FROM field AREA 0 0 0 TO 1 1 1 WITH DATA")
}
