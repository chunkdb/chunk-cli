package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCLIBackupStatement(t *testing.T) {
	canonicalTemp := func() string {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	backups := canonicalTemp()
	s := startServerConfigured(t, false, true, 4, "--backup-dir", backups)
	s.ok(t, "CREATE TABLE world (n u8) CHUNK 2 x 2")
	s.ok(t, "SET BLOCK 0 0 IN world n = 7")
	target := filepath.Join(backups, "it's-backup")
	statement := "BACKUP TO '" + strings.ReplaceAll(filepath.Base(target), "'", "''") + "'"
	code, output, errout := s.cli(t, "", "--json", statement)
	if code != 0 || errout != "" {
		t.Fatalf("backup: %d %q %q", code, output, errout)
	}
	var reply struct {
		Tables uint64 `json:"tables"`
		Files  uint64 `json:"files"`
		Bytes  uint64 `json:"bytes"`
		Cuts   []struct {
			Table, Epoch string
			Revision     uint64
		} `json:"cuts"`
	}
	if err := json.Unmarshal([]byte(output), &reply); err != nil {
		t.Fatal(err)
	}
	if reply.Tables != uint64(len(reply.Cuts)) || reply.Files == 0 || reply.Bytes == 0 {
		t.Fatalf("counts: %s", output)
	}
	found := false
	for _, cut := range reply.Cuts {
		if cut.Table == "world" && len(cut.Epoch) == 32 && cut.Revision > 0 {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing world cut: %s", output)
	}
	if _, err := os.Stat(filepath.Join(target, "chunkdb.backup")); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(backups, "shell-backup")
	code, output, errout = s.cli(t, "BACKUP TO '"+filepath.Base(next)+"'\nquit\n", "shell")
	if code != 0 || errout != "" || !strings.Contains(output, "cuts") {
		t.Fatalf("shell backup: %d %q %q", code, output, errout)
	}
	if _, err := os.Stat(filepath.Join(next, "chunkdb.backup")); err != nil {
		t.Fatal(err)
	}
}
