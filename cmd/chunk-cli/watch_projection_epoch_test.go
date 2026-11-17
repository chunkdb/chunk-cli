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
	for _, mode := range []string{"start-race", "resync", "schema-then-rename", "unprojected-schema-then-add", "schema-then-drop", "two-schemas-then-rename", "same-epoch-rename"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			unprojected := mode == "unprojected-schema-then-add"
			metadata := admissionColumn("owner", "text(8)", 1) + admissionColumn("counter", "u8", 2)
			describe := func(epoch string, version int, columns string, count int) string {
				return strings.Replace(admissionDescribe(epoch, version, columns, count, 2), watchWireBulk("chunk_columns")+"*0\r\n", watchWireBulk("chunk_columns")+"*2\r\n"+metadata, 1)
			}
			schemaPush := func(version int, columns string) string {
				return ">2\r\n" + watchWireBulk("schema") + watchWireMap("epoch", watchWireBulk(epochB), "revision", ":0\r\n", "schema_version", fmt.Sprintf(":%d\r\n", version), "dims", ":2\r\n", "columns", columns, "chunk_columns", "*2\r\n"+metadata)
			}
			initial := describe(watchEpoch, 1, admissionColumn("n", "u8", 1)+admissionColumn("label", "text(8)", 2), 2)
			// The fallback description answers the lookup for the change below.
			startEpoch, eventEpoch, version, events := epochB, epochB, 1, ""
			columns, count := admissionColumn("unused", "u8", 1)+admissionColumn("n", "u8", 3)+admissionColumn("label", "text(8)", 4)+admissionColumn("later", "u8", 5), 4
			renamed := admissionColumn("unused", "u8", 1) + admissionColumn("n", "u8", 3) + admissionColumn("caption", "text(8)", 4) + admissionColumn("later", "u8", 5)
			projected := "*2\r\n" + admissionColumn("label", "text(8)", 4) + admissionColumn("n", "u8", 3)
			want := map[string]any{"label": "chosen", "n": float64(7)}
			var wantErr string
			switch mode {
			case "resync":
				startEpoch = watchEpoch
				events = ">5\r\n" + watchWireBulk("resync") + watchWireBulk(epochB) + ":0\r\n_\r\n_\r\n"
			case "schema-then-rename":
				version, columns = 2, renamed
				events = schemaPush(1, projected)
				want = map[string]any{"caption": "chosen", "n": float64(7)}
			case "unprojected-schema-then-add":
				version, columns = 2, renamed
				events = schemaPush(1, "*3\r\n"+admissionColumn("unused", "u8", 1)+admissionColumn("n", "u8", 3)+admissionColumn("label", "text(8)", 4))
				want = map[string]any{"unused": float64(2), "n": float64(7), "caption": "chosen", "later": float64(11)}
			case "schema-then-drop":
				// The pushed projection owns the epoch; its column 4 is gone
				// even though another column now has the requested name.
				version = 2
				columns = admissionColumn("unused", "u8", 1) + admissionColumn("n", "u8", 3) + admissionColumn("label", "text(8)", 6) + admissionColumn("later", "u8", 5)
				events = schemaPush(1, projected)
				wantErr = "WATCH: projected column 4 no longer exists"
			case "two-schemas-then-rename":
				// The newest accepted push in the epoch binds the projection.
				version = 3
				columns = admissionColumn("unused", "u8", 1) + admissionColumn("n", "u8", 3) + admissionColumn("older", "text(8)", 4) + admissionColumn("newer", "text(8)", 6)
				events = schemaPush(1, projected) + schemaPush(2, "*2\r\n"+admissionColumn("label", "text(8)", 6)+admissionColumn("n", "u8", 3))
				want = map[string]any{"newer": "chosen", "n": float64(7)}
			case "same-epoch-rename":
				// IDs bound by the initial description survive a rename that
				// moves the requested name to a new column.
				startEpoch, eventEpoch, version = watchEpoch, watchEpoch, 2
				columns, count = admissionColumn("n", "u8", 1)+admissionColumn("caption", "text(8)", 2)+admissionColumn("label", "text(8)", 3), 3
				want = map[string]any{"caption": "chosen", "n": float64(7)}
			}
			fallback := describe(eventEpoch, version, columns, count)
			args := []string{"world", "--columns", "label,n", "--json", "--no-reconnect"}
			if unprojected {
				args = []string{"world", "--json", "--no-reconnect"}
			}
			watch, err := parseWatchArgs(args, false)
			if err != nil {
				t.Fatal(err)
			}
			row := "*2\r\n" + watchWireBulk("chosen") + ":7\r\n"
			if unprojected {
				row = "*4\r\n:2\r\n:7\r\n" + watchWireBulk("chosen") + ":11\r\n"
			}
			events += ">10\r\n" + watchWireBulk("change") + watchWireBulk(eventEpoch) + fmt.Sprintf(":1\r\n:123\r\n_\r\n:%d\r\n*1\r\n*4\r\n:0\r\n:1\r\n_\r\n", version) + row + "_\r\n_\r\n*1\r\n*4\r\n:0\r\n:1\r\n_\r\n*2\r\n" + watchWireBulk("kept") + ":9\r\n"
			uri, reached, release, done, described := epochWatchFixture(t, ctx, initial, fallback, startEpoch, eventEpoch, true, false, false, epochWatchFrames{watch.statement, events})
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
			if wantErr != "" {
				select {
				case err := <-result:
					if err == nil || err.Error() != wantErr {
						t.Fatalf("got %v, want %s", err, wantErr)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				waitSlotServer(t, done)
				if described.Load() != 2 || strings.Contains(out.String(), `"type":"change"`) {
					t.Fatalf("descriptions %d, output %s", described.Load(), out.String())
				}
				return
			}
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
			if described.Load() != 2 || len(event.Blocks) != 1 || !reflect.DeepEqual(event.Blocks[0].After, want) || len(event.ChunkChanges) != 1 || !reflect.DeepEqual(event.ChunkChanges[0].After, map[string]any{"owner": "kept", "counter": float64(9)}) {
				t.Fatalf("descriptions %d, change %s", described.Load(), change)
			}
		})
	}
}
