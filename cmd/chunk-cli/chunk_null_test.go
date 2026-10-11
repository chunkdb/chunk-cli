package main

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetChunkNull(t *testing.T) {
	describe := watchWireMap("table", watchWireBulk("world"), "version", ":1\r\n", "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", "%0\r\n", "columns", "*1\r\n"+slotWireColumn())
	for _, tc := range []struct {
		name    string
		options statementOptions
		want    string
	}{
		{"human", statementOptions{}, "(null)\n"},
		{"blocks", statementOptions{blocks: true}, "(null)\n"},
		{"json", statementOptions{json: true}, "null\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uri, done := fakeMigrationServer(t, []string{"DESCRIBE world", "GET CHUNK 0 0 FROM world"}, []string{describe, "_\r\n"})
			client, err := connect(globalOptions{URI: uri, Timeout: time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = execute(client, nil, "GET CHUNK 0 0 FROM world", tc.options, &out, console{})
			_ = client.Close()
			if serverErr := <-done; serverErr != nil {
				t.Fatal(serverErr)
			}
			if err != nil || out.String() != tc.want {
				t.Fatalf("got %q, %v; want %q", out.String(), err, tc.want)
			}
		})
	}
}

func TestGetChunkNullTransactionAndExport(t *testing.T) {
	describe := watchWireMap("table", watchWireBulk("world"), "version", ":1\r\n", "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", "%0\r\n", "columns", "*1\r\n"+slotWireColumn())
	t.Run("transaction", func(t *testing.T) {
		uri, done := fakeMigrationServer(t, []string{"DESCRIBE world", "GET CHUNK 0 0 FROM world"}, []string{describe, "_\r\n"})
		client, err := connect(globalOptions{URI: uri, Timeout: time.Second}, console{})
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		err = execute(client, &shellTxn{open: true}, "GET CHUNK 0 0 FROM world", statementOptions{}, &out, console{})
		_ = client.Close()
		if serverErr := <-done; serverErr != nil {
			t.Fatal(serverErr)
		}
		if err != nil || out.String() != "(null)\n" {
			t.Fatalf("read confused with queued write: %q, %v", out.String(), err)
		}
	})
	t.Run("export", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "chunk.bin")
		original := []byte("existing form")
		if err := os.WriteFile(file, original, 0600); err != nil {
			t.Fatal(err)
		}
		uri, done := fakeMigrationServer(t, []string{"GET CHUNK 0 0 FROM world"}, []string{"_\r\n"})
		client, err := connect(globalOptions{URI: uri, Timeout: time.Second}, console{})
		if err != nil {
			t.Fatal(err)
		}
		err = execute(client, nil, "GET CHUNK 0 0 FROM world", statementOptions{out: file}, &bytes.Buffer{}, console{})
		_ = client.Close()
		if serverErr := <-done; serverErr != nil {
			t.Fatal(serverErr)
		}
		saved, readErr := os.ReadFile(file)
		if err == nil || !strings.Contains(err.Error(), "--out needs a reply of bytes") || readErr != nil || !bytes.Equal(saved, original) {
			t.Fatalf("export: %v, %v, %q", err, readErr, saved)
		}
	})
}

func TestGetChunkVersionedEmpty(t *testing.T) {
	describe := watchWireMap("table", watchWireBulk("world"), "version", ":1\r\n", "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", "%0\r\n", "columns", "*1\r\n"+slotWireColumn())
	form := make([]byte, 21)
	binary.LittleEndian.PutUint64(form, 7)
	binary.LittleEndian.PutUint64(form[8:], 1)
	uri, done := fakeMigrationServer(t, []string{"DESCRIBE world", "GET CHUNK 0 0 FROM world"}, []string{describe, watchWireBulk(string(form))})
	client, err := connect(globalOptions{URI: uri, Timeout: time.Second}, console{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = execute(client, nil, "GET CHUNK 0 0 FROM world", statementOptions{json: true, blocks: true}, &out, console{})
	_ = client.Close()
	if serverErr := <-done; serverErr != nil {
		t.Fatal(serverErr)
	}
	if err != nil || !strings.Contains(out.String(), `"version":7`) || !strings.Contains(out.String(), `"present":0`) {
		t.Fatalf("empty form lost: %q, %v", out.String(), err)
	}
}
