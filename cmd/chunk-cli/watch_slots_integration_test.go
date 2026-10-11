package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os/exec"
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
	createdFrontier := fixtureTableFrontier(t, s)
	if out := s.ok(t, "CREATE SLOT 'consumer' ON world"); out != "OK\n" {
		t.Fatal(out)
	}
	show := s.ok(t, "SHOW SLOTS ON world")
	if !strings.Contains(show, "name = consumer") || !strings.Contains(show, "lost = false") || slotAcked(t, s) != createdFrontier {
		t.Fatal(show)
	}
	uri, proxy := watchProxy(t, s)
	p := startWatchProcess(t, uri, "--slot", "consumer")
	trace := fixtureStart(t, p, false)
	if trace.frontier != createdFrontier {
		t.Fatalf("slot start %d; created at %d", trace.frontier, createdFrontier)
	}
	version := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
	revision := fixtureVersion(t, version)
	header, block := trace.waitMutation(t, p, "change", revision, 1)
	if trace.schemas != 1 || !strings.Contains(header, "change revision "+version+" ") || block != "  block 0 0: (absent) -> {id = 7}" {
		t.Fatalf("output: %q %q; schemas %d", header, block, trace.schemas)
	}
	mandatory := len(trace.eligible)
	trace.stop(t, p, revision, 1)
	if err := fixtureBatchACKs(proxy.text(), trace.eligible, mandatory, 1); err != nil {
		t.Fatal(err)
	}
	if slotAcked(t, s) != revision || !strings.Contains(proxy.text(), "ACK "+version+"\r\n") || !strings.HasSuffix(proxy.text(), "UNWATCH\r\n") {
		t.Fatalf("acknowledgement: %q", proxy.text())
	}
	t.Logf("SHOW SLOTS:\n%s", s.ok(t, "SHOW SLOTS ON world"))
	resumed := startWatchProcess(t, s.uri, "--slot", "consumer", "--json")
	var event struct {
		Type     string        `json:"type"`
		Position watchPosition `json:"position"`
	}
	replay := fixtureStart(t, resumed, true)
	if replay.frontier != revision || replay.epoch != trace.epoch {
		t.Fatalf("resumed position: %v", replay.raw)
	}
	nextVersion := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 8"))
	nextRevision := fixtureVersion(t, nextVersion)
	line, _ := replay.waitMutation(t, resumed, "change", nextRevision, 1)
	if err := json.Unmarshal([]byte(line), &event); err != nil || event.Type != "change" || fmt.Sprint(event.Position.Revision) != nextVersion || !strings.Contains(line, `"before":{"id":7}`) || !strings.Contains(line, `"after":{"id":8}`) || replay.schemas != 1 {
		t.Fatalf("resumed change: %s %v", line, err)
	}
	replay.stop(t, resumed, nextRevision, 1)
	after := event.Position
	withAfter := startWatchProcess(t, s.uri, "--slot", "consumer", "--after", after.Epoch+":"+fmt.Sprint(after.Revision))
	afterTrace := fixtureStart(t, withAfter, false)
	if afterTrace.raw[0] != "start "+after.Epoch+":"+fmt.Sprint(after.Revision) {
		t.Fatal(afterTrace.raw)
	}
	afterTrace.stop(t, withAfter, nextRevision, 1)
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
	trace := fixtureStart(t, p, true)
	firstRevision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
	first, _ := trace.waitMutation(t, p, "change", firstRevision, 1)
	if !strings.Contains(first, `"before":null,"after":{"id":7}`) {
		t.Fatal(first)
	}
	version := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 8"))
	revision := fixtureVersion(t, version)
	second, _ := trace.waitMutation(t, p, "change", revision, 1)
	if !strings.Contains(second, `"before":{"id":7},"after":{"id":8}`) {
		t.Fatal(second)
	}
	// Progress can complete a batch. Compare actual ACKs with complete eligible
	// events instead of treating every pre-cancel ACK as premature.
	mandatory := len(trace.eligible)
	trace.stop(t, p, revision, 1)
	if err := fixtureBatchACKs(proxy.text(), trace.eligible, mandatory, 3); err != nil {
		t.Fatal(err)
	}
	if slotAcked(t, s) != revision || !strings.Contains(proxy.text(), "ACK "+version+"\r\n") || !strings.HasSuffix(proxy.text(), "UNWATCH\r\n") {
		t.Fatalf("partial batch: %q", proxy.text())
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

// A terminal error arriving after printed output and its ACK must survive the
// actual subprocess exit path. The wire ACK is the ordering signal.
func TestCLIWatchSlotTerminalError(t *testing.T) {
	for _, code := range []string{"SLOT_LOST", "NO_TABLE"} {
		t.Run(code, func(t *testing.T) {
			uri, done := fakeSlotServer(t, func(conn net.Conn, reader *bufio.Reader) error {
				if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireSchema(1)+slotWireChange(1)); err != nil {
					return err
				}
				if err := expectSlotLine(reader, "ACK 1"); err != nil {
					return err
				}
				_, err := io.WriteString(conn, "-ERR "+code+" terminal slot error\r\n")
				return err
			})
			p := startWatchProcess(t, uri, "--slot", "consumer")
			for _, want := range []string{
				"start " + watchEpoch + ":0",
				"schema " + watchEpoch + ":1 version 1",
				"change revision 1 time_ms 123 user (anonymous)",
				"  block 0 0: (absent) -> {v = 7}",
			} {
				if got := p.line(t); got != want {
					t.Fatalf("terminal slot output: got %q; want %q", got, want)
				}
			}
			select {
			case err := <-p.done:
				p.finished = true
				exit, ok := err.(*exec.ExitError)
				if !ok || exit.ExitCode() != 1 || p.stderr.String() != "error: "+code+" terminal slot error\n" {
					t.Fatalf("terminal slot exit: %v; stderr %q", err, p.stderr.String())
				}
			case <-time.After(8 * time.Second):
				t.Fatal("terminal slot error did not end the CLI subprocess")
			}
			for extra := range p.lines {
				t.Fatalf("output after terminal slot error: %q", extra)
			}
			waitSlotServer(t, done)
		})
	}
}
