package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

func slotAcked(t *testing.T, s *testServer) uint64 {
	t.Helper()
	var rows []struct {
		Table         string `json:"table"`
		Name          string `json:"name"`
		Epoch         string `json:"epoch"`
		Acked         uint64 `json:"acked"`
		RetainedBytes uint64 `json:"retained_bytes"`
		Lost          bool   `json:"lost"`
	}
	out := s.ok(t, "--json", "SHOW SLOTS ON world")
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 || rows[0].Table != "world" || rows[0].Name != "consumer" || len(rows[0].Epoch) != 32 || rows[0].Lost {
		t.Fatalf("SHOW SLOTS: %q %v", out, err)
	}
	return rows[0].Acked
}

func TestCLIWatchSlotsIntegration(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	if out := s.ok(t, "CREATE SLOT 'consumer' ON world"); out != "OK\n" {
		t.Fatal(out)
	}
	show := s.ok(t, "SHOW SLOTS ON world")
	if !strings.Contains(show, "name = consumer") || !strings.Contains(show, "lost = false") || slotAcked(t, s) != 0 {
		t.Fatal(show)
	}
	uri, proxy := watchProxy(t, s)
	p := startWatchProcess(t, uri, "--slot", "consumer")
	start := p.line(t)
	if !strings.HasPrefix(start, "start ") {
		t.Fatal(start)
	}
	version := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
	schema, header, block := p.line(t), p.line(t), p.line(t)
	if !strings.HasPrefix(schema, "schema ") || !strings.Contains(header, "change revision "+version+" ") || block != "  block 0 0: (absent) -> {id = 7}" {
		t.Fatalf("output: %q %q %q", schema, header, block)
	}
	p.interrupt(t)
	revision, err := strconv.ParseUint(version, 10, 64)
	if err != nil || slotAcked(t, s) != revision || strings.Count(proxy.text(), "ACK "+version+"\r\n") != 1 || !strings.Contains(proxy.text(), "ACK "+version+"\r\nUNWATCH\r\n") {
		t.Fatalf("acknowledgement: %q %v", proxy.text(), err)
	}
	t.Logf("Observed slot output:\n%s\n%s\n%s\n%s\nSHOW SLOTS:\n%s", start, schema, header, block, s.ok(t, "SHOW SLOTS ON world"))
	resumed := startWatchProcess(t, s.uri, "--slot", "consumer", "--json")
	var event struct {
		Type     string        `json:"type"`
		Position watchPosition `json:"position"`
	}
	if err := json.Unmarshal([]byte(resumed.line(t)), &event); err != nil || event.Type != "start" || event.Position.Revision != revision {
		t.Fatalf("resumed start: %+v %v", event, err)
	}
	nextVersion := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 8"))
	if line := resumed.line(t); !strings.Contains(line, `"type":"schema"`) {
		t.Fatal(line)
	}
	line := resumed.line(t)
	if err := json.Unmarshal([]byte(line), &event); err != nil || event.Type != "change" || fmt.Sprint(event.Position.Revision) != nextVersion || !strings.Contains(line, `"before":{"id":7}`) || !strings.Contains(line, `"after":{"id":8}`) {
		t.Fatalf("resumed change: %s %v", line, err)
	}
	resumed.interrupt(t)
	after := event.Position
	withAfter := startWatchProcess(t, s.uri, "--slot", "consumer", "--after", after.Epoch+":"+fmt.Sprint(after.Revision))
	if line := withAfter.line(t); line != "start "+after.Epoch+":"+fmt.Sprint(after.Revision) {
		t.Fatal(line)
	}
	withAfter.interrupt(t)
	if out := s.ok(t, "DROP SLOT 'consumer' ON world"); out != "OK\n" {
		t.Fatal(out)
	}
	if out := s.ok(t, "SHOW SLOTS ON world"); out != "(empty)\n" {
		t.Fatal(out)
	}
	code, out, errout := s.cli(t, "CREATE SLOT 'consumer' ON world\nSHOW SLOTS ON world\nDROP SLOT 'consumer' ON world\nquit\n", "shell")
	if code != 0 || errout != "" || !strings.Contains(out, "name = consumer") || strings.Count(out, "OK\n") != 2 {
		t.Fatalf("shell slot statements: %d %q %q", code, out, errout)
	}
}

func TestCLIWatchSlotPartialBatch(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	s.ok(t, "CREATE SLOT 'consumer' ON world")
	uri, proxy := watchProxy(t, s)
	p := startWatchProcess(t, uri, "--slot", "consumer", "--ack-every", "3", "--json")
	_ = p.line(t)
	s.ok(t, "SET BLOCK 0 0 IN world id = 7")
	_ = p.line(t)
	_ = p.line(t)
	version := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 8"))
	if line := p.line(t); !strings.Contains(line, `"revision":`+version) {
		t.Fatal(line)
	}
	if strings.Contains(proxy.text(), "ACK ") {
		t.Fatalf("premature batch ACK: %q", proxy.text())
	}
	p.interrupt(t)
	revision, err := strconv.ParseUint(version, 10, 64)
	if err != nil || slotAcked(t, s) != revision || !strings.Contains(proxy.text(), "ACK "+version+"\r\nUNWATCH\r\n") {
		t.Fatalf("partial batch: %q %v", proxy.text(), err)
	}
}

func TestCLIWatchSlotLost(t *testing.T) {
	s := startServerConfigured(t, false, true, 4, "--slot-max-bytes", "1", "--checkpoint-updates", "1")
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	s.ok(t, "CREATE SLOT 'consumer' ON world")
	s.ok(t, "SET BLOCK 0 0 IN world id = 7")
	s.ok(t, "FLUSH WAL")
	p := startWatchProcess(t, s.uri, "--slot", "consumer")
	select {
	case err := <-p.done:
		p.finished = true
		if err == nil || !strings.Contains(p.stderr.String(), "SLOT_LOST") {
			t.Fatalf("lost slot: %v %q", err, p.stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("lost slot did not end the command")
	}
	if out := s.ok(t, "SHOW SLOTS ON world"); !strings.Contains(out, "lost = true") {
		t.Fatal(out)
	}
}
