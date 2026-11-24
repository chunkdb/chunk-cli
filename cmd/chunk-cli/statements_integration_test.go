package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCLIServerStatementFields reads the fields 2.0 added to DESCRIBE,
// SHOW STATUS and SHOW TABLE STATS through the CLI.
func TestCLIServerStatementFields(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE units (hp i16 CHECK (hp >= 0), max_hp i16 NULL) CHUNK 2 x 2 CHECK hp_max (hp <= max_hp)")
	out := s.ok(t, "DESCRIBE units")
	if !strings.Contains(out, "\nchecks:\n  hp_check (") || !strings.Contains(out, "\n  hp_max (") || strings.Contains(out, "fenced") {
		t.Fatalf("DESCRIBE: %q", out)
	}
	if out := s.ok(t, "--json", "DESCRIBE units"); !strings.Contains(out, `"checks":[{"name":`) || !strings.Contains(out, `{"name":"hp_check","expression":`) {
		t.Fatalf("DESCRIBE --json: %q", out)
	}

	out = s.ok(t, "SHOW STATUS")
	for _, part := range []string{"version = ", "\nuptime_ms = ", "\nread_only = false\n", "\n    name = units\n    state = ok\n    reason = NULL\n", "\nfenced = []\n", "\nactivation_pending = "} {
		if !strings.Contains(out, part) {
			t.Fatalf("SHOW STATUS lacks %q:\n%s", part, out)
		}
	}

	s.ok(t, "SET BLOCK 0 0 IN units hp = 5")
	s.ok(t, "CREATE SLOT 'consumer' ON units")
	var stats map[string]any
	if err := json.Unmarshal([]byte(s.ok(t, "--json", "SHOW TABLE STATS units")), &stats); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]float64{"dims": 2, "chunks": 1, "tombstones": 0, "slots": 1, "watches": 0} {
		if stats[key] != want {
			t.Fatalf("SHOW TABLE STATS %s = %v, want %v: %v", key, stats[key], want, stats)
		}
	}
	if files, ok := stats["files"].(float64); !ok || files < 1 {
		t.Fatalf("SHOW TABLE STATS files: %v", stats)
	}
	out = s.ok(t, "SHOW TABLE STATS units")
	if !strings.HasPrefix(out, "epoch = ") || !strings.Contains(out, "\nchunks = 1\n") || !strings.Contains(out, "\nslots = 1\n") {
		t.Fatalf("SHOW TABLE STATS: %q", out)
	}
}

// TestCLIWatchCatalog streams WATCH CATALOG: the whole catalog first, then
// each change with the user that made it.
func TestCLIWatchCatalog(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (kind u8) CHUNK 2 x 2")

	p := startWatchCommand(t, s.uri, "", "--catalog")
	if line := p.line(t); !strings.HasPrefix(line, "start ") {
		t.Fatalf("start: %q", line)
	}
	if line := p.line(t); !strings.HasPrefix(line, "catalog revision ") || strings.Contains(line, " user ") {
		t.Fatalf("whole catalog: %q", line)
	}
	s.ok(t, "CREATE TABLE second (kind u8) CHUNK 2 x 2")
	var lines []string
	for {
		line := p.line(t)
		lines = append(lines, line)
		if line == "  create table second" {
			break
		}
	}
	if !containsLine(lines, "  create table world") {
		t.Fatalf("whole catalog lacks world: %q", lines)
	}
	if header := lines[len(lines)-2]; !strings.HasPrefix(header, "catalog revision ") || !strings.HasSuffix(header, " user "+adminUser) {
		t.Fatalf("change header: %q", lines)
	}
	p.interrupt(t)

	j := startWatchCommand(t, s.uri, "", "--catalog", "--json")
	var start struct {
		Type     string
		Position struct{ Epoch string }
	}
	if err := json.Unmarshal([]byte(j.line(t)), &start); err != nil || start.Type != "start" || len(start.Position.Epoch) != 32 {
		t.Fatalf("JSON start: %+v %v", start, err)
	}
	var whole struct {
		Type   string
		User   *string
		Events []struct{ Kind, Action, Name string }
	}
	if err := json.Unmarshal([]byte(j.line(t)), &whole); err != nil || whole.Type != "catalog" || whole.User != nil {
		t.Fatalf("JSON catalog: %+v %v", whole, err)
	}
	var tables []string
	for _, event := range whole.Events {
		if event.Kind == "table" && event.Action == "create" {
			tables = append(tables, event.Name)
		}
	}
	if strings.Join(tables, ",") != "second,world" && strings.Join(tables, ",") != "world,second" {
		t.Fatalf("JSON catalog tables: %+v", whole.Events)
	}
	j.interrupt(t)

	s.fails(t, "takes no table", "watch", "--catalog", "world")
}

func containsLine(lines []string, want string) bool {
	for _, line := range lines {
		if line == want {
			return true
		}
	}
	return false
}
