package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCLIWorldExample(t *testing.T) {
	s := startServer(t, false)
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(t.TempDir(), "chunk-cli")
	build := exec.Command("go", "build", "-o", cli, "./cmd/chunk-cli")
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build example CLI: %v\n%s", err, out)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "bash", filepath.Join(root, "examples", "world.sh"))
	command.Dir = root
	command.Env = append(os.Environ(), "CHUNKDB_URI="+s.uri, "CHUNKCLI_BIN="+cli)
	out, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("world example: %v\n%s", err, out)
	}
	t.Logf("world example output:\n%s", out)
	text := string(out)
	if !strings.Contains(text, "kind = 1\nname = 'grass'") || !strings.Contains(text, "block 1 1") {
		t.Fatalf("missing block/area output: %s", out)
	}
	found := false
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var event struct {
			Type   string `json:"type"`
			Blocks []struct {
				X, Y          int
				Before, After struct {
					Kind int
					Name string
				}
			} `json:"blocks"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == "change" {
			if len(event.Blocks) != 1 || event.Blocks[0].X != 1 || event.Blocks[0].Y != 1 || event.Blocks[0].Before.Kind != 1 || event.Blocks[0].Before.Name != "grass" || event.Blocks[0].After.Kind != 2 || event.Blocks[0].After.Name != "door" {
				t.Fatalf("unexpected example change: %s", line)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("missing watch change: %s", out)
	}
}
