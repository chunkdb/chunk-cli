package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWatchProjectionEpochBinding(t *testing.T) {
	for _, mode := range []string{"start-race", "resync", "schema-then-rename", "unprojected-schema-then-add"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			announced := strings.Contains(mode, "schema-then")
			unprojected := strings.HasPrefix(mode, "unprojected")
			version, label := 1, "label"
			if announced {
				version, label = 2, "caption"
			}
			metadata := admissionColumn("owner", "text(8)", 1) + admissionColumn("counter", "u8", 2)
			describe := func(epoch string, version int, columns string, count int) string {
				return strings.Replace(admissionDescribe(epoch, version, columns, count, 2), watchWireBulk("chunk_columns")+"*0\r\n", watchWireBulk("chunk_columns")+"*2\r\n"+metadata, 1)
			}
			initial := describe(watchEpoch, 1, admissionColumn("n", "u8", 1)+admissionColumn("label", "text(8)", 2), 2)
			columns := admissionColumn("unused", "u8", 1) + admissionColumn("n", "u8", 3) + admissionColumn(label, "text(8)", 4) + admissionColumn("later", "u8", 5)
			fallback := describe(epochB, version, columns, 4)
			args := []string{"world", "--columns", "label,n", "--json", "--no-reconnect"}
			if unprojected {
				args = []string{"world", "--json", "--no-reconnect"}
			}
			watch, err := parseWatchArgs(args, false)
			if err != nil {
				t.Fatal(err)
			}
			startEpoch, events := epochB, ""
			if mode == "resync" {
				startEpoch = watchEpoch
				events = ">5\r\n" + watchWireBulk("resync") + watchWireBulk(epochB) + ":0\r\n_\r\n_\r\n"
			}
			if announced {
				projected := "*2\r\n" + admissionColumn("label", "text(8)", 4) + admissionColumn("n", "u8", 3)
				if unprojected {
					projected = "*3\r\n" + admissionColumn("unused", "u8", 1) + admissionColumn("n", "u8", 3) + admissionColumn("label", "text(8)", 4)
				}
				events += ">2\r\n" + watchWireBulk("schema") + watchWireMap("epoch", watchWireBulk(epochB), "revision", ":0\r\n", "schema_version", ":1\r\n", "dims", ":2\r\n", "columns", projected, "chunk_columns", "*2\r\n"+metadata)
			}
			row := "*2\r\n" + watchWireBulk("chosen") + ":7\r\n"
			want := map[string]any{label: "chosen", "n": float64(7)}
			if unprojected {
				row = "*4\r\n:2\r\n:7\r\n" + watchWireBulk("chosen") + ":11\r\n"
				want["unused"], want["later"] = float64(2), float64(11)
			}
			events += ">10\r\n" + watchWireBulk("change") + watchWireBulk(epochB) + fmt.Sprintf(":1\r\n:123\r\n_\r\n:%d\r\n*1\r\n*4\r\n:0\r\n:1\r\n_\r\n", version) + row + "_\r\n_\r\n*1\r\n*4\r\n:0\r\n:1\r\n_\r\n*2\r\n" + watchWireBulk("kept") + ":9\r\n"
			uri, reached, release, done, count := epochWatchFixture(t, ctx, initial, fallback, startEpoch, epochB, true, false, false, epochWatchFrames{watch.statement, events})
			out := &slotOutput{writes: make(chan string, 8)}
			result := make(chan error, 1)
			go func() {
				result <- runWatch(ctx, globalOptions{URI: uri, Timeout: 2 * time.Second}, watch, console{}, out)
			}()
			select {
			case <-reached:
			case err := <-result:
				t.Fatal(err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			close(release)
			var change string
			for change == "" {
				select {
				case body := <-out.writes:
					if strings.Contains(body, `"type":"change"`) {
						change = body
					}
				case err := <-result:
					t.Fatalf("projected epoch fallback: %v; output %s", err, out.String())
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			cancel()
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("WATCH did not stop")
			}
			waitSlotServer(t, done)
			event := readModelJSON[struct {
				Blocks       []struct{ After map[string]any }
				ChunkChanges []struct{ After map[string]any } `json:"chunk_changes"`
			}](t, change)
			if count.Load() != 2 || len(event.Blocks) != 1 || !reflect.DeepEqual(event.Blocks[0].After, want) || len(event.ChunkChanges) != 1 || !reflect.DeepEqual(event.ChunkChanges[0].After, map[string]any{"owner": "kept", "counter": float64(9)}) {
				t.Fatalf("descriptions %d, change %s", count.Load(), change)
			}
		})
	}
}
