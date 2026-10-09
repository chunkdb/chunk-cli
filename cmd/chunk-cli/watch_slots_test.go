package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func slotWireColumn() string {
	return watchWireMap("id", ":1\r\n", "name", watchWireBulk("v"), "type", watchWireBulk("u8"), "null", "#f\r\n", "required", "#f\r\n", "default", "_\r\n")
}
func slotWireSchema(revision uint64) string {
	return ">5\r\n" + watchWireBulk("schema") + watchWireBulk(watchEpoch) + fmt.Sprintf(":%d\r\n:1\r\n*1\r\n", revision) + slotWireColumn()
}
func slotWireChange(revision uint64) string {
	return ">7\r\n" + watchWireBulk("change") + watchWireBulk(watchEpoch) + fmt.Sprintf(":%d\r\n:123\r\n_\r\n:1\r\n*1\r\n*4\r\n:0\r\n:0\r\n_\r\n*1\r\n:7\r\n", revision)
}

// fakeSlotServer performs the ordinary DESCRIBE connection followed by the
// dedicated stream connection, then lets a test check its wire commands.
func fakeSlotServer(t *testing.T, stream func(net.Conn, *bufio.Reader) error) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		for i := 0; i < 2; i++ {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			r := bufio.NewReader(conn)
			if err = expectSlotLine(r, "HELLO 3"); err != nil {
				done <- err
				return
			}
			hello := watchWireMap("protocol", ":3\r\n", "server_version", watchWireBulk("2.0"), "max_line_bytes", ":1048576\r\n", "max_parameters", ":1024\r\n", "max_area_chunks", ":4096\r\n", "max_response_bytes", ":67108864\r\n", "max_scan_limit", ":4096\r\n", "server_signature", "_\r\n")
			if _, err = io.WriteString(conn, hello); err != nil {
				done <- err
				return
			}
			if i == 0 {
				if err = expectSlotLine(r, "DESCRIBE world"); err != nil {
					done <- err
					return
				}
				describe := watchWireMap("table", watchWireBulk("world"), "version", ":1\r\n", "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", "%0\r\n", "columns", "*1\r\n"+slotWireColumn())
				if _, err = io.WriteString(conn, describe); err != nil {
					done <- err
					return
				}
				continue
			}
			if err = expectSlotLine(r, "WATCH world SLOT 'consumer'"); err != nil {
				done <- err
				return
			}
			done <- stream(conn, r)
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return "chunk://" + listener.Addr().String() + "/", done
}

func expectSlotLine(r *bufio.Reader, want string) error {
	line, err := r.ReadString('\n')
	if err != nil || line != want+"\r\n" {
		return fmt.Errorf("expected %q, got %q: %v", want, line, err)
	}
	return nil
}
func waitSlotServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("fake slot server did not finish")
	}
}

// slotOutput observes complete output writes and can stop or fail a selected
// write. A change includes its entire human block listing in one write.
type slotOutput struct {
	bytes.Buffer
	writes            chan string
	blocked           chan struct{}
	release           chan struct{}
	failType          string
	short             bool
	cancelAfterChange context.CancelFunc
}

func (w *slotOutput) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

func (w *slotOutput) Write(p []byte) (int, error) {
	s := string(p)
	if w.blocked != nil && (strings.HasPrefix(s, "change ") || strings.Contains(s, `"type":"change"`)) {
		close(w.blocked)
		<-w.release
		w.blocked = nil
	}
	if w.failType != "" && (strings.HasPrefix(s, w.failType+" ") || strings.Contains(s, `"type":"`+w.failType+`"`)) {
		if w.short {
			return len(p) - 1, nil
		}
		return 0, errors.New("output failed")
	}
	n, err := w.Buffer.Write(p)
	if w.cancelAfterChange != nil && strings.HasPrefix(s, "change ") {
		w.cancelAfterChange()
	}
	if w.writes != nil {
		w.writes <- s
	}
	return n, err
}

func TestSlotWatchCancelDuringCompletedWriteFlushesPosition(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
		if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireChange(1)); err != nil {
			return err
		}
		if err := expectSlotLine(r, "ACK 1"); err != nil {
			return err
		}
		if err := expectSlotLine(r, "UNWATCH"); err != nil {
			return err
		}
		_, err := io.WriteString(conn, "+OK\r\n")
		return err
	})
	out := &slotOutput{cancelAfterChange: cancel}
	if err := runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 2}, console{}, out); err != nil {
		t.Fatal(err)
	}
	waitSlotServer(t, done)
}

func TestSlotWatchAcknowledgesAfterPrinting(t *testing.T) {
	for _, json := range []bool{false, true} {
		t.Run(fmt.Sprint(json), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			out := &slotOutput{blocked: make(chan struct{}), release: make(chan struct{})}
			command := make(chan string, 1)
			uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
				_, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireSchema(1)+slotWireChange(1))
				if err != nil {
					return err
				}
				line, err := r.ReadString('\n')
				command <- line
				if err != nil || line != "ACK 1\r\n" {
					return fmt.Errorf("ACK: %q %v", line, err)
				}
				cancel()
				if err = expectSlotLine(r, "UNWATCH"); err != nil {
					return err
				}
				_, err = io.WriteString(conn, "+OK\r\n")
				return err
			})
			result := make(chan error, 1)
			blocked := out.blocked
			go func() {
				result <- runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 1, json: json}, console{}, out)
			}()
			select {
			case <-blocked:
			case <-time.After(3 * time.Second):
				t.Fatal("change output was not reached")
			}
			select {
			case line := <-command:
				t.Fatalf("acknowledged before change output completed: %q", line)
			default:
			}
			close(out.release)
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			waitSlotServer(t, done)
		})
	}
}

func TestSlotWatchBatchesAndFlushesOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := &slotOutput{writes: make(chan string, 8)}
	uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
		_, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireSchema(1)+slotWireChange(1)+slotWireSchema(2)+slotWireChange(2))
		if err != nil {
			return err
		}
		if err = expectSlotLine(r, "ACK 2"); err != nil {
			return err
		}
		if _, err = io.WriteString(conn, slotWireChange(3)); err != nil {
			return err
		}
		for line := range out.writes {
			if strings.Contains(line, "change revision 3 ") {
				break
			}
		}
		// Receiving output precedes the pending-state update. Wait for its
		// mutex to be taken by the reader by sending another schema frame.
		if _, err = io.WriteString(conn, slotWireSchema(4)); err != nil {
			return err
		}
		for line := range out.writes {
			if strings.Contains(line, "schema "+watchEpoch+":4 ") {
				break
			}
		}
		cancel()
		if err = expectSlotLine(r, "ACK 3"); err != nil {
			return err
		}
		if err = expectSlotLine(r, "UNWATCH"); err != nil {
			return err
		}
		_, err = io.WriteString(conn, "+OK\r\n")
		return err
	})
	if err := runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 2}, console{}, out); err != nil {
		t.Fatal(err)
	}
	waitSlotServer(t, done)
}

func TestSlotWatchOutputFailureDoesNotAcknowledge(t *testing.T) {
	for _, json := range []bool{false, true} {
		for _, kind := range []string{"start", "schema", "change"} {
			for _, short := range []bool{false, true} {
				t.Run(fmt.Sprintf("json=%t/%s/short=%t", json, kind, short), func(t *testing.T) {
					uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
						if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireSchema(1)+slotWireChange(1)); err != nil {
							return err
						}
						line, err := r.ReadString('\n')
						if line != "" || err == nil {
							return fmt.Errorf("command after failed output: %q %v", line, err)
						}
						return nil
					})
					out := &slotOutput{failType: kind, short: short}
					err := runWatch(t.Context(), globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 1, json: json}, console{}, out)
					if err == nil || (short && !errors.Is(err, io.ErrShortWrite)) || (!short && !strings.Contains(err.Error(), "output failed")) {
						t.Fatalf("output error: %v", err)
					}
					waitSlotServer(t, done)
				})
			}
		}
	}
}

func TestSlotWatchReportsAckError(t *testing.T) {
	uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
		if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+slotWireChange(1)); err != nil {
			return err
		}
		if err := expectSlotLine(r, "ACK 1"); err != nil {
			return err
		}
		_, err := io.WriteString(conn, "-INVALID_ARGUMENT acknowledgement too high\r\n")
		return err
	})
	err := runWatch(t.Context(), globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 1}, console{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "INVALID_ARGUMENT") {
		t.Fatalf("ACK error: %v", err)
	}
	waitSlotServer(t, done)
}
