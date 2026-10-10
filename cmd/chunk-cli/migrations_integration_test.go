package main

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestCLIMigrationsConcurrentAndConflict(t *testing.T) {
	for _, tls := range []bool{false, true} {
		name := "plain"
		if tls {
			name = "tls"
		}
		t.Run(name, func(t *testing.T) {
			s := startServer(t, tls)
			file := migrationFile(t, "-- migrate: create_world\nCREATE TABLE world (id u8) CHUNK 2 x 2\n-- migrate: add_name\nALTER TABLE world ADD COLUMN name text(16) NULL\n-- migrate: create_slot\nCREATE SLOT 'consumer' ON world")
			type result struct {
				code         int
				out, failure string
			}
			start := make(chan struct{})
			done := make(chan result, 2)
			for range 2 {
				go func() {
					<-start
					code, out, failure := s.cli(t, "", "migrate", file)
					done <- result{code, out, failure}
				}()
			}
			close(start)
			counts := map[string]int{}
			for range 2 {
				r := <-done
				if r.code != 0 || r.failure != "" {
					t.Fatalf("concurrent migration: %d %q %q", r.code, r.out, r.failure)
				}
				for _, line := range strings.Split(strings.TrimSpace(r.out), "\n") {
					counts[line]++
				}
			}
			for _, step := range []string{"create_world", "add_name", "create_slot"} {
				if counts[step+" applied"] != 1 || counts[step+" skipped"] != 1 {
					t.Fatalf("step %s: %#v", step, counts)
				}
			}
			if out := s.ok(t, "migrate", file); out != "create_world skipped\nadd_name skipped\ncreate_slot skipped\n" {
				t.Fatalf("repeat: %q", out)
			}
			if out := s.ok(t, "DESCRIBE world"); !strings.Contains(out, "name text(16) NULL") {
				t.Fatalf("schema: %q", out)
			}
			if out := s.ok(t, "SHOW SLOTS ON world"); !strings.Contains(out, "name = consumer") {
				t.Fatalf("slots: %q", out)
			}
			var records []struct {
				Name      string `json:"name"`
				Applied   int64  `json:"applied_ms"`
				User      string `json:"user"`
				Statement string `json:"statement"`
			}
			if err := json.Unmarshal([]byte(s.ok(t, "--json", "migrations")), &records); err != nil {
				t.Fatal(err)
			}
			if len(records) != 3 {
				t.Fatalf("records: %#v", records)
			}
			for i, step := range []string{"create_world", "add_name", "create_slot"} {
				if records[i].Name != step || records[i].Applied <= 0 || records[i].User != adminUser || records[i].Statement == "" {
					t.Fatalf("record %d: %#v", i, records[i])
				}
			}
			conflict := migrationFile(t, "-- migrate: create_world\nCREATE TABLE world (id u16) CHUNK 2 x 2\n-- migrate: later\nCREATE TABLE later (id u8) CHUNK 2 x 2")
			s.fails(t, "migration \"create_world\": CONFLICT", "migrate", conflict)
			s.fails(t, "NO_TABLE", "DESCRIBE later")
			if err := json.Unmarshal([]byte(s.ok(t, "--json", "migrations")), &records); err != nil || len(records) != 3 {
				t.Fatalf("conflict recorded: %#v %v", records, err)
			}
		})
	}
}

func TestCLIMigrationStopsAfterSemanticError(t *testing.T) {
	s := startServerWith(t, false, false)
	file := migrationFile(t, "-- migrate: create_world\nCREATE TABLE world (id u8) CHUNK 2 x 2\n-- migrate: absent\nALTER TABLE missing ADD COLUMN id u8\n-- migrate: later\nCREATE TABLE later (id u8) CHUNK 2 x 2")
	code, out, failure := s.cli(t, "", "migrate", file)
	if code != 1 || out != "create_world applied\n" || !strings.Contains(failure, "migration \"absent\": NO_TABLE") {
		t.Fatalf("got %d %q %q", code, out, failure)
	}
	s.fails(t, "NO_TABLE", "DESCRIBE later")
	var records []map[string]any
	if err := json.Unmarshal([]byte(s.ok(t, "--json", "migrations")), &records); err != nil || len(records) != 1 || records[0]["user"] != "" {
		t.Fatalf("records: %#v %v", records, err)
	}
}

func TestCLIRawMigrateStatements(t *testing.T) {
	s := startServer(t, false)
	for i, keyword := range []string{"MIGRATE", "Migrate", "MiGrAtE", "migrate"} {
		name := "raw_" + strconv.Itoa(i)
		statement := "CREATE TABLE " + name + " (id u8) CHUNK 2 x 2"
		if out := s.ok(t, keyword, "'"+name+"'", statement); out != "applied\n" {
			t.Fatalf("%s apply: %q", keyword, out)
		}
		if out := s.ok(t, keyword, "'"+name+"'", statement); out != "skipped\n" {
			t.Fatalf("%s skip: %q", keyword, out)
		}
		s.ok(t, "DESCRIBE "+name)
	}
}
