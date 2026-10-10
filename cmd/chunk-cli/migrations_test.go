package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

func TestParseMigrations(t *testing.T) {
	text := "-- comment\r\n\r\n-- migrate: create_world\r\n  create  TABLE world (\r\n id u8,\r\n name text(32) DEFAULT 'it''s; $1  here') CHUNK 2 x 2\r\n-- migrate: checkpoint\r\nALTER TABLE world SET checkpoint_ms = 512\r\n"
	got, err := parseMigrations(strings.NewReader(text))
	want := []migrationStep{{"create_world", "create  TABLE world ( id u8, name text(32) DEFAULT 'it''s; $1  here') CHUNK 2 x 2"}, {"checkpoint", "ALTER TABLE world SET checkpoint_ms = 512"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v", got, err)
	}
	for _, statement := range []string{"CREATE TABLE w (id u8)", "ALTER TABLE w ADD id u8", "DROP TABLE w", "GRANT READ ON w TO bot", "REVOKE READ ON w FROM bot", "CREATE SLOT 'reader' ON w", "DROP SLOT 'reader' ON w"} {
		if _, err := parseMigrations(strings.NewReader("-- migrate: _1\n" + statement)); err != nil {
			t.Errorf("%q: %v", statement, err)
		}
	}
	for _, name := range []string{"a", "_", "_0", strings.Repeat("a", 63)} {
		if !validMigrationName(name) {
			t.Errorf("rejected %q", name)
		}
	}
}

func TestParseMigrationsRejectsInvalidFiles(t *testing.T) {
	for _, text := range []string{
		"", "-- comment", "CREATE TABLE w (id u8)", "-- migrate: a", "-- migrate: A\nDROP TABLE w", "-- migrate: 0a\nDROP TABLE w",
		"-- migrate: " + strings.Repeat("a", 64) + "\nDROP TABLE w", "-- migrate: x' DROP TABLE w\nDROP TABLE w",
		"-- migrate: a\nDROP TABLE w\n-- migrate: a\nDROP TABLE w", "-- migrate: a\nPING", "-- migrate: a\nMIGRATE 'b' DROP TABLE w",
		"-- migrate: a\nCREATE USER bot VERIFIER $1", "-- migrate: a\nALTER TABLE w ADD text text(8) DEFAULT $1",
		"-- migrate: a\nDROP TABLE w; DROP TABLE z", "-- migrate: a\nALTER TABLE w ADD t text(8) DEFAULT 'a\nb'",
		"-- migrate: a\nDROP TA\rBLE w", "-- migrate: a\nDROP TABLE w\x00", "-- migrate: a\nDROP TABLE " + strings.Repeat("a", 65536),
	} {
		if _, err := parseMigrations(strings.NewReader(text)); err == nil {
			t.Errorf("accepted invalid file %q", text[:min(len(text), 100)])
		}
	}
	if _, err := parseMigrations(errorMigrationReader{}); err == nil || !strings.Contains(err.Error(), "broken input") {
		t.Fatalf("reader error: %v", err)
	}
}

type errorMigrationReader struct{}

func (errorMigrationReader) Read([]byte) (int, error) { return 0, errors.New("broken input") }

func migrationFile(t *testing.T, text string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "migrations.cql")
	if err := os.WriteFile(file, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

func fakeMigrationServer(t *testing.T, commands, replies []string) (string, <-chan error) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		r := bufio.NewReader(conn)
		if err := expectSlotLine(r, "HELLO 3"); err != nil {
			done <- err
			return
		}
		hello := watchWireMap("protocol", ":3\r\n", "server_version", watchWireBulk("2.0"), "max_line_bytes", ":1048576\r\n", "max_parameters", ":1024\r\n", "max_area_chunks", ":4096\r\n", "max_response_bytes", ":67108864\r\n", "max_scan_limit", ":4096\r\n", "server_signature", "_\r\n")
		if _, err := io.WriteString(conn, hello); err != nil {
			done <- err
			return
		}
		for i, command := range commands {
			if err := expectSlotLine(r, command); err != nil {
				done <- err
				return
			}
			if _, err := io.WriteString(conn, replies[i]); err != nil {
				done <- err
				return
			}
		}
		line, err := r.ReadString('\n')
		if !errors.Is(err, io.EOF) || line != "" {
			done <- fmt.Errorf("unexpected later command %q, %v", line, err)
			return
		}
		done <- nil
	}()
	t.Cleanup(func() { listener.Close() })
	return "chunk://" + listener.Addr().String() + "/", done
}

func TestMigrateFakeServer(t *testing.T) {
	file := migrationFile(t, "-- migrate: a\nCREATE TABLE w (id u8)\n-- migrate: b\nALTER TABLE w ADD n u8\n-- migrate: c\nDROP TABLE w")
	for _, tc := range []struct {
		name         string
		replies      []string
		json         bool
		code         int
		out, failure string
	}{
		{"applied_skipped", []string{"+applied\r\n", "+skipped\r\n", "+applied\r\n"}, false, 0, "a applied\nb skipped\nc applied\n", ""},
		{"json", []string{"+skipped\r\n", "+applied\r\n", "+skipped\r\n"}, true, 0, "{\"name\":\"a\",\"status\":\"skipped\"}\n{\"name\":\"b\",\"status\":\"applied\"}\n{\"name\":\"c\",\"status\":\"skipped\"}\n", ""},
		{"conflict_stops", []string{"+applied\r\n", "-ERR CONFLICT b differs\r\n"}, false, 1, "a applied\n", "migration \"b\": CONFLICT b differs"},
		{"wrong_status_stops", []string{"+applied\r\n", "+OK\r\n"}, false, 1, "a applied\n", "migration \"b\": expected applied or skipped"},
		{"wrong_type_stops", []string{"$7\r\napplied\r\n"}, false, 1, "", "migration \"a\": expected applied or skipped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := []string{"MIGRATE 'a' CREATE TABLE w (id u8)", "MIGRATE 'b' ALTER TABLE w ADD n u8", "MIGRATE 'c' DROP TABLE w"}
			uri, done := fakeMigrationServer(t, commands[:len(tc.replies)], tc.replies)
			args := []string{"--uri", uri}
			if tc.json {
				args = append(args, "--json")
			}
			args = append(args, "migrate", file)
			var out, failure bytes.Buffer
			if code := run(args, strings.NewReader(""), &out, &failure); code != tc.code || out.String() != tc.out || !strings.Contains(failure.String(), tc.failure) {
				t.Fatalf("got %d, %q, %q", code, out.String(), failure.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func TestMigratePreservesServerError(t *testing.T) {
	uri, done := fakeMigrationServer(t, []string{"MIGRATE 'a' DROP TABLE w"}, []string{"-ERR CONFLICT a differs\r\n"})
	client, err := connect(globalOptions{URI: uri, Timeout: time.Second}, console{in: strings.NewReader(""), out: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	err = runMigrations(client, []migrationStep{{"a", "DROP TABLE w"}}, io.Discard, false)
	var serverErr *chunkclient.ServerError
	if !errors.As(err, &serverErr) || serverErr.Code != "CONFLICT" {
		t.Fatalf("lost server error: %v", err)
	}
	client.Close()
	waitSlotServer(t, done)
}

func TestMigrationsFakeServer(t *testing.T) {
	record := watchWireMap("name", watchWireBulk("a"), "applied_ms", ":123\r\n", "user", watchWireBulk(""), "statement", watchWireBulk("DROP TABLE w"))
	for _, tc := range []struct{ reply, want, failure string }{
		{"*0\r\n", "(empty)\n", ""},
		{"*1\r\n" + record, "name = a, applied_ms = 123, user = , statement = DROP TABLE w", ""},
		{"+OK\r\n", "", "expected an array"},
		{"*1\r\n+OK\r\n", "", "expected a record map"},
		{"*1\r\n%0\r\n", "", "expected bulk string name"},
		{"*1\r\n" + watchWireMap("name", watchWireBulk("a"), "user", watchWireBulk(""), "statement", watchWireBulk("DROP TABLE w"), "applied_ms", watchWireBulk("123")), "", "expected integer applied_ms"},
	} {
		uri, done := fakeMigrationServer(t, []string{"SHOW MIGRATIONS"}, []string{tc.reply})
		var out, failure bytes.Buffer
		code := run([]string{"--uri", uri, "migrations"}, strings.NewReader(""), &out, &failure)
		wantCode := 0
		if tc.failure != "" {
			wantCode = 1
		}
		if code != wantCode || !strings.Contains(out.String(), tc.want) || !strings.Contains(failure.String(), tc.failure) {
			t.Fatalf("got %d, %q, %q", code, out.String(), failure.String())
		}
		waitSlotServer(t, done)
	}
}

func TestMigrateValidatesBeforeConnecting(t *testing.T) {
	badFile := migrationFile(t, "-- migrate: a\nDROP TABLE w\n-- migrate: b\nPING")
	for _, args := range [][]string{{"migrate"}, {"migrate", "a", "b"}, {"migrations", "a"}, {"migrate", badFile}, {"--in", "x", "migrate", badFile}, {"--blocks", "migrations"}, {"--out", "x", "migrations"}, {"--new-password-file", "x", "migrations"}, {"migrate", filepath.Join(t.TempDir(), "missing")}} {
		var out, failure bytes.Buffer
		code := run(append([]string{"--uri", "http://invalid/"}, args...), strings.NewReader(""), &out, &failure)
		if code != 1 || strings.Contains(failure.String(), "unsupported scheme") {
			t.Fatalf("%v connected: %d, %q", args, code, failure.String())
		}
	}
}
