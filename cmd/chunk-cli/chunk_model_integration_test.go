package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIChunkModel3D(t *testing.T) {
	for _, tls := range []bool{false, true} {
		name := "plain"
		if tls {
			name = "TLS"
		}
		t.Run(name, func(t *testing.T) {
			s := startServerWith(t, tls, false)
			s.ok(t, "CREATE TABLE space (kind u8) CHUNK 2 x 2 x 2 CHUNK COLUMNS (owner text(16) NULL, flag bool DEFAULT TRUE)")
			owner := filepath.Join(t.TempDir(), "owner.txt")
			if err := os.WriteFile(owner, []byte("base"), 0600); err != nil {
				t.Fatal(err)
			}
			s.ok(t, "--in", owner, `SET CHUNK 0 0 0 IN space META owner=$1 IF ABSENT`)
			s.ok(t, "SET BLOCK 1 0 1 IN space kind=7")
			read := func() map[string]any {
				t.Helper()
				var result map[string]any
				if err := json.Unmarshal([]byte(s.ok(t, "--json", "--blocks", "GET CHUNK 0 0 0 FROM space")), &result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			chunk := read()
			metadata := chunk["chunk_columns"].(map[string]any)
			if metadata["owner"] != "base" || metadata["flag"] != true || chunk["commit_time_ms"].(float64) <= 0 {
				t.Fatal(chunk)
			}
			block := chunk["blocks"].([]any)[0].(map[string]any)
			if block["x"] != float64(1) || block["y"] != float64(0) || block["z"] != float64(1) {
				t.Fatal(block)
			}
			full := filepath.Join(t.TempDir(), "full.bin")
			partial := filepath.Join(t.TempDir(), "partial.bin")
			s.ok(t, "--out", full, "GET CHUNK 0 0 0 FROM space")
			s.ok(t, "--out", partial, "GET CHUNK 0 0 0 FROM space COLUMNS kind")
			if data, err := os.ReadFile(full); err != nil || !bytes.HasPrefix(data, []byte("CKCF")) {
				t.Fatal("not a checked form", err)
			}
			if err := os.WriteFile(owner, []byte("changed"), 0600); err != nil {
				t.Fatal(err)
			}
			s.ok(t, "--in", owner, `SET CHUNK 0 0 0 IN space META owner=$1`)
			s.ok(t, "--in", partial, `SET CHUNK 0 0 0 IN space COLUMNS kind $1`)
			if read()["chunk_columns"].(map[string]any)["owner"] != "changed" {
				t.Fatal("partial import replaced metadata")
			}
			s.ok(t, "--in", full, `SET CHUNK 1 0 0 IN space $1 IF ABSENT`)
			if output := s.ok(t, "--json", "GET AREA 0 0 0 TO 1 0 0 FROM space"); !strings.Contains(output, `"cz":0`) || !strings.Contains(output, `"owner":"changed"`) || !strings.Contains(output, `"owner":"base"`) {
				t.Fatal(output)
			}
			code, output, errorOutput := s.cli(t, "BEGIN\n--json GET CHUNK 0 0 0 FROM space\nSET BLOCK 1 0 1 IN space kind=8\n--json GET CHUNK 0 0 0 FROM space\nROLLBACK\n", "shell")
			if code != 0 || errorOutput != "" || !strings.Contains(output, `"private":true`) || !strings.Contains(output, `"commit_time_ms":null`) {
				t.Fatal(code, output, errorOutput)
			}
			s.ok(t, "CREATE TABLE plain (kind u8) CHUNK 1 x 1 x 1")
			s.ok(t, "SET BLOCK 0 0 0 IN plain kind=1")
			s.ok(t, "DELETE BLOCK 0 0 0 FROM plain")
			output = s.ok(t, "--json", "SCAN CHUNKS FROM plain SINCE 0 WITH DATA")
			if !strings.Contains(output, `"deleted":true,"chunk":null`) || !strings.Contains(output, `"retained_deletion_floor":0`) {
				t.Fatal(output)
			}
		})
	}
}
