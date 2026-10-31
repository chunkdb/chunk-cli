package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func fixtureProgressWire(revision uint64) string {
	return ">3\r\n" + watchWireBulk("progress") + watchWireBulk(watchEpoch) + fmt.Sprintf(":%d\r\n", revision)
}

// Exercise the actual decoder, formatter, subprocess cancellation and docs
// drain against the unchanged marked FEED fence. ACK is the explicit signal
// that the final progress was completely printed before cancellation.
func TestDocWatchInterleavedProgress(t *testing.T) {
	source, err := os.ReadFile("../../docs/FEED.md")
	if err != nil {
		t.Fatal(err)
	}
	examples, err := parseDocExamples(string(source))
	if err != nil {
		t.Fatal(err)
	}
	var expected string
	for _, example := range examples {
		if example.kind == "sh docs-check-watch" {
			expected = example.expected
		}
	}
	revision, version, err := fixtureDocMutation(expected)
	if err != nil {
		t.Fatal(err)
	}
	for _, beforeSchema := range []bool{true, false} {
		t.Run(fmt.Sprintf("progress_before_schema=%t", beforeSchema), func(t *testing.T) {
			printed := make(chan struct{})
			recorder := &recordingProxy{}
			uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
				schema := strings.Replace(slotWireSchema(revision), watchWireBulk("v"), watchWireBulk("kind"), 1)
				prefix := schema + fixtureProgressWire(revision-1)
				if beforeSchema {
					prefix = fixtureProgressWire(revision-1) + schema
				}
				change := ">7\r\n" + watchWireBulk("change") + watchWireBulk(watchEpoch) + fmt.Sprintf(":%d\r\n:123\r\n", revision) + watchWireBulk("admin") + ":1\r\n*1\r\n*4\r\n:1\r\n:1\r\n*1\r\n:1\r\n*1\r\n:6\r\n"
				if _, err := io.WriteString(conn, "+OK "+watchEpoch+fmt.Sprintf(" %d\r\n", revision-1)+prefix+change+fixtureProgressWire(revision)); err != nil {
					return err
				}
				ack, err := r.ReadString('\n')
				if err != nil || ack != fmt.Sprintf("ACK %d\r\n", revision) {
					return fmt.Errorf("registered ACK: %q %v", ack, err)
				}
				if _, err := recorder.Write([]byte(ack)); err != nil {
					return err
				}
				close(printed)
				if err := expectSlotLine(r, "UNWATCH"); err != nil {
					return err
				}
				_, err = io.WriteString(conn, "+OK\r\n")
				return err
			})
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			code := quote(os.Args[0]) + " -test.run='^TestWatchProcess$' -- --uri " + quote(uri) + " watch world --slot consumer --ack-every 3"
			w := startDocWatch(t, code, t.TempDir(), append(os.Environ(), "CHUNKCLI_WATCH_PROCESS=1"))
			trace, err := newWatchFixture(strings.TrimSuffix(w.line(t, "synthetic start"), "\n"), false)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = trace.mutation(func(deadline time.Time) (string, error) {
				line, err := readWatchFixtureLine(w.lines, deadline)
				return strings.TrimSuffix(line, "\n"), err
			}, "change", revision, version)
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-printed:
			case <-time.After(5 * time.Second):
				t.Fatal("final progress was not completely printed and ACKed")
			}
			trace.awaitACK(t, recorder, w.lines, revision, version)
			w.finish(t, func(line string) error { return trace.drain(strings.TrimSuffix(line, "\n"), revision, version) })
			waitSlotServer(t, done)
			if got := normalizeDocOutput(strings.Join(trace.semantic, "\n") + "\n"); got != expected {
				t.Fatalf("marked transcript mismatch\nwant:\n%s\ngot:\n%s", expected, got)
			}
			if len(trace.eligible) != 3 || trace.eligible[0] != revision-1 || trace.eligible[1] != revision || trace.eligible[2] != revision || len(trace.raw) != 6 {
				t.Fatalf("lost raw/progress evidence: %+v", trace)
			}
			t.Logf("Raw synthetic WATCH stream:\n%s", strings.Join(trace.raw, "\n"))
		})
	}
}

func TestWatchFixtureProgressValidation(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, tc := range []struct {
			name     string
			revision string
			epoch    string
			drain    bool
		}{
			{"malformed", "bad", watchEpoch, false}, {"overflow", "18446744073709551616", watchEpoch, false},
			{"regressed", "1", watchEpoch, false}, {"ahead_of_mutation", "3", watchEpoch, false},
			{"wrong_epoch", "2", strings.Repeat("f", 32), false}, {"ahead_during_drain", "4", watchEpoch, true},
		} {
			t.Run(fmt.Sprintf("json=%t/%s", jsonOutput, tc.name), func(t *testing.T) {
				start := "start " + watchEpoch + ":2"
				line := "progress " + tc.epoch + ":" + tc.revision
				if jsonOutput {
					start = fmt.Sprintf(`{"type":"start","position":{"epoch":%q,"revision":2}}`, watchEpoch)
					line = fmt.Sprintf(`{"type":"progress","position":{"epoch":%q,"revision":%q}}`, tc.epoch, tc.revision)
				}
				trace, err := newWatchFixture(start, jsonOutput)
				if err != nil {
					t.Fatal(err)
				}
				if tc.drain {
					trace.frontier = 3
					err = trace.drain(line, 3, 1)
				} else {
					lines := make(chan string, 1)
					lines <- line
					close(lines)
					_, _, err = trace.mutation(func(deadline time.Time) (string, error) { return readWatchFixtureLine(lines, deadline) }, "change", 3, 1)
				}
				if err == nil || len(trace.eligible) != 0 || len(trace.raw) != 2 || trace.raw[1] != line {
					t.Fatalf("invalid progress accepted/lost: %v %+v", err, trace)
				}
			})
		}
	}
	t.Run("drain_retains_all_lines_after_error", func(t *testing.T) {
		lines := make(chan string, 3)
		for _, line := range []string{"bad", "progress " + watchEpoch + ":3", "unexpected change"} {
			lines <- line
		}
		close(lines)
		done := make(chan error, 1)
		done <- nil
		trace, _ := newWatchFixture("start "+watchEpoch+":3", false)
		err := drainWatchFixture(lines, done, time.Now().Add(time.Second), func(line string) error { return trace.drain(line, 3, 1) })
		if err == nil || len(trace.raw) != 4 || trace.raw[3] != "unexpected change" {
			t.Fatalf("drain lost evidence: %v %+v", err, trace)
		}
	})
}

func TestWatchFixtureBatchAccounting(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		eligible   []uint64
		valid      bool
	}{
		{"partial_without_progress", "ACK 4\r\nUNWATCH\r\n", []uint64{3, 4}, true},
		{"progress_completes_batch", "ACK 4\r\nUNWATCH\r\n", []uint64{2, 3, 4}, true},
		{"repeated_idle_progress", "ACK 3\r\nACK 4\r\nUNWATCH\r\n", []uint64{2, 2, 3, 4}, true},
		{"post_cancel_tail", "ACK 4\r\nUNWATCH\r\n", []uint64{3, 4, 4}, true},
		{"premature_batch", "ACK 3\r\nACK 4\r\nUNWATCH\r\n", []uint64{3, 4}, false},
		{"invented_frontier", "ACK 5\r\nUNWATCH\r\n", []uint64{2, 3, 4}, false},
		{"missing_final_partial", "UNWATCH\r\n", []uint64{3, 4}, false},
		{"last_visible_unregistered", "UNWATCH\r\n", []uint64{3}, true},
		{"only_last_may_be_unregistered", "ACK 3\r\nUNWATCH\r\n", []uint64{3, 4, 5}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fixtureBatchACKs(tc.wire, tc.eligible, 3)
			if (err == nil) != tc.valid {
				t.Fatalf("ACK oracle: %v", err)
			}
		})
	}
}

// Two changes leave a partial batch. A complete progress write reaches its
// threshold, while a failed/short progress write must never publish that ACK.
func TestSlotWatchProgressCompletesBatch(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, outcome := range []string{"complete", "failed", "short"} {
			t.Run(fmt.Sprintf("json=%t/%s", jsonOutput, outcome), func(t *testing.T) {
				uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
					if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireSchema(1)+slotWireChange(1)+slotWireChange(2)+fixtureProgressWire(2)); err != nil {
						return err
					}
					if outcome == "complete" {
						return expectSlotLine(r, "ACK 2")
					}
					line, err := r.ReadString('\n')
					if err != io.EOF || line != "" {
						return fmt.Errorf("ACK before complete progress write: %q %v", line, err)
					}
					return nil
				})
				out := &slotOutput{}
				if outcome != "complete" {
					out.failType = "progress"
					out.short = outcome == "short"
				}
				err := runWatch(t.Context(), globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 3, json: jsonOutput, noReconnect: true}, console{}, out)
				if outcome == "complete" {
					if !errors.Is(err, io.EOF) {
						t.Fatal(err)
					}
				} else if err == nil || !(strings.Contains(err.Error(), "output failed") || strings.Contains(err.Error(), "short write")) {
					t.Fatal(err)
				}
				waitSlotServer(t, done)
			})
		}
	}
}

// The existing writer hook makes a complete row visible and cancels before
// Write returns. UNWATCH with no ACK is legal only for this final event.
func TestWatchFixtureVisibleCancellationBoundary(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	captured := make(chan string, 1)
	uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
		if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireChange(1)); err != nil {
			return err
		}
		line, err := r.ReadString('\n')
		captured <- line
		if err != nil || line != "UNWATCH\r\n" {
			return fmt.Errorf("boundary wire: %q %v", line, err)
		}
		_, err = io.WriteString(conn, "+OK\r\n")
		return err
	})
	out := &slotOutput{cancelAfterChange: cancel}
	if err := runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 3}, console{}, out); err != nil {
		t.Fatal(err)
	}
	waitSlotServer(t, done)
	trace, err := newWatchFixture("start "+watchEpoch+":0", false)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")[1:]
	_, _, err = trace.mutation(func(time.Time) (string, error) {
		if len(lines) == 0 {
			return "", io.EOF
		}
		line := lines[0]
		lines = lines[1:]
		return line, nil
	}, "change", 1, 1)
	if err != nil || len(trace.eligible) != 1 {
		t.Fatalf("visible complete mutation: %v %v", err, trace.raw)
	}
	wire := <-captured
	if err := fixtureBatchACKs(wire, trace.eligible, 3); err != nil {
		t.Fatal(err)
	}
	if frontier := fixtureSavedFrontier(t, wire, 0); frontier != 0 {
		t.Fatalf("unproven saved frontier %d", frontier)
	}
	t.Logf("Complete visible mutation remains unregistered: raw %v; wire %q; saved frontier 0", trace.raw, wire)
}

func TestWatchFixtureCapturedACKRequiresCompleteLine(t *testing.T) {
	recorder := &recordingProxy{}
	_, _ = recorder.Write([]byte("ACK 3"))
	if acknowledged, _, err := recorder.acknowledged(3); err != nil || acknowledged {
		t.Fatalf("partial wire is not ACK proof: %t %v", acknowledged, err)
	}
	_, _ = recorder.Write([]byte("\r\n"))
	if acknowledged, _, err := recorder.acknowledged(3); err != nil || !acknowledged {
		t.Fatalf("complete captured ACK: %t %v", acknowledged, err)
	}
}

func TestWatchFixtureResyncCurrentIdentity(t *testing.T) {
	current := "fedcba9876543210fedcba9876543210"
	f := &watchFixture{epoch: watchEpoch}
	line := "resync " + current + ":17; restart with --after " + current + ":17"
	if err := f.resync(line, current, 17); err != nil {
		t.Fatal(err)
	}
	if f.epoch != current || f.frontier != 17 || len(f.eligible) != 0 || len(f.semantic) != 1 {
		t.Fatalf("resync state: %+v", f)
	}
	for _, bad := range []string{
		"resync " + watchEpoch + ":17; restart",
		"resync " + current + ":16; restart",
		"resync " + current + ":18; restart",
		"resync invalid:17; restart",
	} {
		before := len(f.semantic)
		if err := f.resync(bad, current, 17); err == nil {
			t.Fatalf("accepted wrong resync: %s", bad)
		}
		if f.epoch != current || f.frontier != 17 || len(f.semantic) != before || len(f.eligible) != 0 {
			t.Fatalf("invalid resync changed state: %+v", f)
		}
	}
}
