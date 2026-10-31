package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type retryExchange struct{ command, reply string }

func retryFixture(t *testing.T, sessions [][]retryExchange) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		for _, session := range sessions {
			conn, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			err = func() error {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(4 * time.Second))
				r := bufio.NewReader(conn)
				if len(session) == 0 || session[0].command != "HELLO 3" {
					session = append([]retryExchange{{"HELLO 3", admissionHello()}}, session...)
				}
				for _, exchange := range session {
					if err := expectSlotLine(r, exchange.command); err != nil {
						return err
					}
					if _, err := io.WriteString(conn, exchange.reply); err != nil {
						return err
					}
				}
				return nil
			}()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	return "chunk://" + listener.Addr().String() + "/", done
}

func TestTransactionFileRetriesHELLORefusal(t *testing.T) {
	for _, code := range []string{"BUSY", "RATE_LIMITED"} {
		t.Run(code, func(t *testing.T) {
			uri, done := retryFixture(t, [][]retryExchange{
				{{"HELLO 3", "-" + code + " temporary admission refusal\r\n"}},
				{{"BEGIN", "+OK\r\n"}, {"PING", "+PONG\r\n"}, {"COMMIT", "_\r\n"}},
			})
			var out, failure bytes.Buffer
			exit := run([]string{"--uri", uri, "transaction", transactionFile(t, "PING\n")}, strings.NewReader(""), &out, &failure)
			if exit != 0 || out.String() != "OK\nPONG\n(nothing written)\n" || failure.Len() != 0 {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), failure.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func TestWatchReconnectPreservesAfterIdentifiers(t *testing.T) {
	for _, table := range []string{"after", "world"} {
		t.Run(table, func(t *testing.T) {
			args := []string{table, "--area", "0,0,1,1", "--columns", "after", "--no-before"}
			if table == "after" {
				args = append(args, "--after", watchEpoch+":0")
			}
			watch, err := parseWatchArgs(args, false)
			if err != nil {
				t.Fatal(err)
			}
			resumed := "WATCH " + table + " AREA 0 0 TO 1 1 COLUMNS after NO BEFORE AFTER " + watchEpoch + " 1"
			uri, done := retryFixture(t, [][]retryExchange{
				{{"DESCRIBE " + table, admissionDescribe(watchEpoch, 1, admissionColumn("after", "u8", 1), 1, 2)}},
				{{watch.statement, "+OK " + watchEpoch + " 0\r\n" + fixtureProgressWire(1)}},
				{{resumed, "+OK " + watchEpoch + " 1\r\n" + fixtureProgressWire(2)}, {"UNWATCH", "+OK\r\n"}},
			})
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			out := &retryProgressOutput{cancel: cancel, revision: 2}
			if err := runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watch, console{}, out); err != nil {
				t.Fatal(err)
			}
			waitSlotServer(t, done)
		})
	}
}

func TestWatchReconnectWaitsForSlotRelease(t *testing.T) {
	statement := "WATCH world SLOT 'consumer'"
	resumed := statement + " AFTER " + watchEpoch + " 1"
	uri, done := retryFixture(t, [][]retryExchange{
		{{"DESCRIBE world", admissionDescribe(watchEpoch, 1, admissionColumn("v", "u8", 1), 1, 2)}},
		{{statement, "+OK " + watchEpoch + " 0\r\n" + fixtureProgressWire(1)}},
		// The previous connection still owns its slot on the first reattach.
		{{resumed, "-SLOT_BUSY previous consumer is still detaching\r\n"}},
		{{resumed, "+OK " + watchEpoch + " 1\r\n" + fixtureProgressWire(2)}, {"UNWATCH", "+OK\r\n"}},
	})
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	out := &retryProgressOutput{cancel: cancel, revision: 2}
	watch := watchOptions{table: "world", statement: statement, slot: "consumer", ackEvery: 10}
	if err := runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watch, console{}, out); err != nil {
		t.Fatal(err)
	}
	waitSlotServer(t, done)
}

func transactionFile(t *testing.T, source string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "transaction.cql")
	if err := os.WriteFile(file, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestTransactionFileRetriesWithoutFailedOutput(t *testing.T) {
	for _, code := range []string{"CONFLICT", "SCHEMA_MISMATCH", "BUSY", "RATE_LIMITED", "DISK_FULL"} {
		t.Run(code, func(t *testing.T) {
			failure := []retryExchange{{"BEGIN", "+OK\r\n"}, {"SET BLOCK 0 0 IN world v = 7", "_\r\n"}, {"COMMIT", "-" + code + " retry\r\n"}}
			success := []retryExchange{{"BEGIN", "+OK\r\n"}, {"SET BLOCK 0 0 IN world v = 7", "_\r\n"}, {"COMMIT", ":9\r\n"}}
			uri, done := retryFixture(t, [][]retryExchange{failure, success})
			file := transactionFile(t, "BEGIN\nSET BLOCK 0 0 IN world v = 7\nCOMMIT\n")
			var out, failureOut bytes.Buffer
			exit := run([]string{"--uri", uri, "--in", file, "transaction"}, strings.NewReader(""), &out, &failureOut)
			if exit != 0 || out.String() != "OK\n(applies at COMMIT)\n9\n" || failureOut.Len() != 0 {
				t.Fatalf("exit=%d out=%q stderr=%q", exit, out.String(), failureOut.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func TestTransactionFileRetryLimitAndDisable(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			attempts := maxRetries + 1
			if disabled {
				attempts = 1
			}
			var sessions [][]retryExchange
			for i := 0; i < attempts; i++ {
				sessions = append(sessions, []retryExchange{{"BEGIN", "-BUSY retry\r\n"}})
			}
			uri, done := retryFixture(t, sessions)
			file := transactionFile(t, "PING\n")
			args := []string{"--uri", uri}
			if disabled {
				args = append(args, "--no-retry")
			}
			args = append(args, "transaction", file)
			var out, failure bytes.Buffer
			exit := run(args, strings.NewReader(""), &out, &failure)
			if exit != 4 || out.Len() != 0 || !strings.Contains(failure.String(), fmt.Sprintf("gave up after %d attempt(s)", attempts)) {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), failure.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func TestTransactionFileDoesNotRetryUnknownCommit(t *testing.T) {
	for _, reply := range []string{"", "-OUTCOME_UNKNOWN unresolved\r\n", "-SYNTAX invalid\r\n"} {
		t.Run(fmt.Sprintf("%q", reply), func(t *testing.T) {
			uri, done := retryFixture(t, [][]retryExchange{{{"BEGIN", "+OK\r\n"}, {"PING", "+PONG\r\n"}, {"COMMIT", reply}}})
			var out, failure bytes.Buffer
			exit := run([]string{"--uri", uri, "transaction", transactionFile(t, "PING\n")}, strings.NewReader(""), &out, &failure)
			if exit == 0 || out.Len() != 0 || strings.Contains(failure.String(), "gave up") {
				t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), failure.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func TestTransactionFileValidation(t *testing.T) {
	for _, source := range []string{"", "BEGIN\nPING\n", "PING\nCOMMIT\n", "BEGIN\nBEGIN\nPING\nCOMMIT\n", "ROLLBACK\n", "CREATE USER bot PASSWORD\n"} {
		if _, err := transactionStatements([]byte(source)); err == nil {
			t.Fatalf("accepted %q", source)
		}
	}
	statements, err := transactionStatements([]byte("-- comment\n\nPING\n"))
	if err != nil || len(statements) != 1 || statements[0] != "PING" {
		t.Fatal(statements, err)
	}
}

func TestTransactionFileStdinAndJSONFailure(t *testing.T) {
	uri, done := retryFixture(t, [][]retryExchange{{{"BEGIN", "-RATE_LIMITED retry_after_ms=50 busy\r\n"}}})
	var out, failure bytes.Buffer
	exit := run([]string{"--uri", uri, "--no-retry", "--json", "--in", "-", "transaction"}, strings.NewReader("PING\n"), &out, &failure)
	if exit != 4 || out.Len() != 0 || !strings.Contains(failure.String(), `"code":"RATE_LIMITED"`) || !strings.Contains(failure.String(), `"retry_after_ms":50`) || !strings.Contains(failure.String(), "gave up after 1 attempt(s)") {
		t.Fatalf("exit=%d stdout=%q stderr=%q", exit, out.String(), failure.String())
	}
	waitSlotServer(t, done)
}

func TestWatchReconnectsAfterLastCompleteOutput(t *testing.T) {
	for _, slot := range []bool{false, true} {
		t.Run(fmt.Sprint(slot), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			statement := "WATCH world"
			if slot {
				statement += " SLOT 'consumer'"
			}
			partial := slotWireChange(4)
			uri, done := retryFixture(t, [][]retryExchange{
				{{"DESCRIBE world", admissionDescribe(watchEpoch, 1, admissionColumn("v", "u8", 1), 1, 2)}},
				{{statement, "+OK " + watchEpoch + " 0\r\n" + slotWireSchema(1) + slotWireChange(3) + partial[:len(partial)/2]}},
				{{statement + " AFTER " + watchEpoch + " 3", "+OK " + watchEpoch + " 3\r\n" + fixtureProgressWire(4)}, {"UNWATCH", "+OK\r\n"}},
			})
			out := &retryProgressOutput{cancel: cancel}
			watch := watchOptions{table: "world", statement: statement, ackEvery: 10}
			if slot {
				watch.slot = "consumer"
			}
			if err := runWatch(ctx, globalOptions{URI: uri, Timeout: time.Second}, watch, console{}, out); err != nil {
				t.Fatal(err)
			}
			if strings.Count(out.String(), "change revision 3 ") != 1 || strings.Contains(out.String(), "change revision 4 ") {
				t.Fatal(out.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func TestWatchReconnectDisabled(t *testing.T) {
	watch, err := parseWatchArgs([]string{"world", "--no-reconnect"}, false)
	if err != nil || !watch.noReconnect {
		t.Fatal(watch, err)
	}
	uri, done := retryFixture(t, [][]retryExchange{
		{{"DESCRIBE world", admissionDescribe(watchEpoch, 1, admissionColumn("v", "u8", 1), 1, 2)}},
		{{"WATCH world", "+OK " + watchEpoch + " 0\r\n"}},
	})
	err = runWatch(t.Context(), globalOptions{URI: uri, Timeout: time.Second}, watch, console{}, io.Discard)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	waitSlotServer(t, done)
}

// Cancellation inside the completed output write fixes the ACK boundary.
type retryProgressOutput struct {
	bytes.Buffer
	cancel   context.CancelFunc
	revision uint64
}

func (w *retryProgressOutput) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if strings.HasPrefix(string(p), "progress ") && (w.revision == 0 || strings.Contains(string(p), fmt.Sprintf(":%d\n", w.revision))) {
		w.cancel()
	}
	return n, err
}
func (w *retryProgressOutput) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
