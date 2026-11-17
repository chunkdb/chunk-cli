package main

import (
	"strings"
	"testing"
)

func TestCLIWatchThreeAxisArea(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (kind u8) CHUNK 2 x 2 x 2")
	uri, proxy := watchProxy(t, s)
	p := startWatchProcess(t, uri, "--area", "0,0,1,0,0,1")
	trace := fixtureStart(t, p, false)
	if !strings.Contains(proxy.text(), "AREA 0 0 1 TO 0 0 1") {
		t.Fatalf("WATCH frame: %q", proxy.text())
	}
	// Chunk 0 0 0 shares cx and cy with the area but lies outside it on z.
	s.ok(t, "SET BLOCK 1 1 1 IN world kind = 4")
	revision := fixtureVersion(t, s.ok(t, "SET BLOCK 1 1 3 IN world kind = 7"))
	_, block := trace.waitMutation(t, p, "change", revision, 1)
	if !strings.Contains(block, "block 1 1 3: (absent) -> {kind = 7}") {
		t.Fatal(block)
	}
	trace.stop(t, p, revision, 1)
}
