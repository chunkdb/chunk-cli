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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

const epochB = "fedcba9876543210fedcba9876543210"
const epochC = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// The initial description and WATCH attach are explicitly gated. Each
// metadata request uses the real CLI constructor and RESP decoder.
func epochWatchFixture(t *testing.T, ctx context.Context, initial, fallback, startEpoch, eventEpoch string, modern, resync, slot bool) (string, <-chan struct{}, chan<- struct{}, <-chan error, *atomic.Int32) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reached := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 8)
	var count atomic.Int32
	var workers sync.WaitGroup
	var mu sync.Mutex
	var sockets []net.Conn
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			sockets = append(sockets, conn)
			mu.Unlock()
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				deadline, _ := ctx.Deadline()
				conn.SetDeadline(deadline)
				r := bufio.NewReader(conn)
				if err := expectSlotLine(r, "HELLO 3"); err != nil {
					done <- err
					return
				}
				if _, err := io.WriteString(conn, admissionHello()); err != nil {
					done <- err
					return
				}
				line, err := r.ReadString('\n')
				if err != nil {
					done <- err
					return
				}
				if line == "DESCRIBE world\r\n" {
					body := initial
					if count.Add(1) > 1 {
						body = fallback
					}
					_, err := io.WriteString(conn, body)
					if err != nil {
						done <- err
					}
					return
				}
				statement := "WATCH world"
				if slot {
					statement += " SLOT 'consumer'"
				}
				if resync {
					statement += " AFTER " + watchEpoch + " 0"
				}
				if line != statement+"\r\n" {
					done <- fmt.Errorf("WATCH attach got %q", line)
					return
				}
				close(reached)
				select {
				case <-release:
				case <-ctx.Done():
					done <- ctx.Err()
					return
				}
				start := "+OK " + startEpoch + " 0\r\n"
				if modern {
					start = watchWireMap("epoch", watchWireBulk(startEpoch), "revision", ":0\r\n")
				}
				event := strings.Replace(slotWireChange(1), watchEpoch, eventEpoch, 1)
				if modern {
					event = strings.Replace(event, ">7\r\n", ">10\r\n", 1) + "_\r\n_\r\n*0\r\n"
				}
				if resync {
					event = ">3\r\n" + watchWireBulk("resync") + watchWireBulk(eventEpoch) + ":0\r\n" + event
				}
				if _, err := io.WriteString(conn, start+event); err != nil {
					done <- err
					return
				}
				line, err = r.ReadString('\n')
				if errors.Is(err, io.EOF) && line == "" {
					done <- nil
					return
				}
				if err != nil || line != "UNWATCH\r\n" {
					done <- fmt.Errorf("unexpected control/ACK %q: %v", line, err)
					return
				}
				_, err = io.WriteString(conn, "+OK\r\n")
				done <- err
			}()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, c := range sockets {
			c.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return "chunk://" + listener.Addr().String() + "/", reached, release, done, &count
}
func TestWatchEpochConstructorReplacement(t *testing.T) {
	for _, modern := range []bool{false, true} {
		for _, jsonOutput := range []bool{false, true} {
			t.Run(fmt.Sprintf("modern=%t/json=%t", modern, jsonOutput), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
				defer cancel()
				initial := admissionDescribe(watchEpoch, 1, admissionColumn("old", "u8", 1), 1, 2)
				fallback := admissionDescribe(epochB, 1, admissionColumn("new", "u8", 1), 1, 2)
				uri, reached, release, done, count := epochWatchFixture(t, ctx, initial, fallback, epochB, epochB, modern, false, false)
				out := &slotOutput{writes: make(chan string, 8)}
				result := make(chan error, 1)
				go func() {
					result <- runWatch(ctx, globalOptions{URI: uri, Timeout: 2 * time.Second}, watchOptions{table: "world", statement: "WATCH world", json: jsonOutput}, console{}, out)
				}()
				select {
				case <-reached:
				case <-ctx.Done():
					t.Fatal("WATCH constructor did not attach", ctx.Err())
				}
				close(release)
				for {
					select {
					case body := <-out.writes:
						if strings.HasPrefix(body, "change ") || strings.Contains(body, "\"type\":\"change\"") {
							cancel()
							goto completed
						}
					case <-ctx.Done():
						t.Fatal("epoch-B change did not print", ctx.Err())
					}
				}
			completed:
				select {
				case err := <-result:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("WATCH did not finish after complete output")
				}
				waitSlotServer(t, done)
				if count.Load() != 2 || strings.Contains(out.String(), "old") || !strings.Contains(out.String(), "new") {
					t.Fatalf("replacement descriptions=%d: %s", count.Load(), out.String())
				}
				if jsonOutput {
					want := "\"after\":{\"new\":7}"
					if !strings.Contains(out.String(), want) {
						t.Fatal(out.String())
					}
				} else if !strings.Contains(out.String(), "  block 0 0: (absent) -> {new = 7}\n") {
					t.Fatal(out.String())
				}
			})
		}
	}
}
func TestWatchEpochLegacyAndUnknownAfter(t *testing.T) {
	for _, mode := range []string{"empty-epoch", "unknown-after"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			initialEpoch, startEpoch, name := "", epochB, "old"
			resync := false
			if mode == "unknown-after" {
				initialEpoch = epochB
				startEpoch = watchEpoch
				name = "new"
				resync = true
			}
			initial := admissionDescribe(initialEpoch, 1, admissionColumn(name, "u8", 1), 1, 2)
			uri, reached, release, done, count := epochWatchFixture(t, ctx, initial, "", startEpoch, epochB, true, resync, false)
			statement := "WATCH world"
			if resync {
				statement += " AFTER " + watchEpoch + " 0"
			}
			out := &slotOutput{writes: make(chan string, 8)}
			result := make(chan error, 1)
			go func() {
				result <- runWatch(ctx, globalOptions{URI: uri, Timeout: 2 * time.Second}, watchOptions{table: "world", statement: statement}, console{}, out)
			}()
			select {
			case <-reached:
			case <-ctx.Done():
				t.Fatal("WATCH attach", ctx.Err())
			}
			close(release)
			for {
				select {
				case body := <-out.writes:
					if strings.HasPrefix(body, "change ") {
						cancel()
						goto completed
					}
				case <-ctx.Done():
					t.Fatal("change did not print", ctx.Err())
				}
			}
		completed:
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("legacy/resync cancellation")
			}
			waitSlotServer(t, done)
			if count.Load() != 1 || !strings.HasPrefix(out.String(), "start "+startEpoch+":0\n") || !strings.Contains(out.String(), "{"+name+" = 7}") {
				t.Fatalf("description authority: %d %s", count.Load(), out.String())
			}
			if resync && !strings.Contains(out.String(), "resync "+epochB+":0;") {
				t.Fatal("lost legal resync", out.String())
			}
		})
	}
}
func TestWatchEpochFallbackRefusalHasNoOutputCacheOrACK(t *testing.T) {
	for _, mode := range []string{"epoch", "version"} {
		t.Run(mode, func(t *testing.T) {
			fallbackEpoch, version := epochC, 1
			if mode == "version" {
				fallbackEpoch = epochB
				version = 2
			}
			ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
			defer cancel()
			initial := admissionDescribe(watchEpoch, 1, admissionColumn("old", "u8", 1), 1, 2)
			fallback := admissionDescribe(fallbackEpoch, version, admissionColumn("wrong", "u8", 1), 1, 2)
			uri, reached, release, done, count := epochWatchFixture(t, ctx, initial, fallback, epochB, epochB, true, false, true)
			var out bytes.Buffer
			result := make(chan error, 1)
			go func() {
				result <- runWatch(ctx, globalOptions{URI: uri, Timeout: 2 * time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 1}, console{}, &out)
			}()
			select {
			case <-reached:
			case <-ctx.Done():
				t.Fatal("WATCH attach", ctx.Err())
			}
			close(release)
			select {
			case err := <-result:
				if err == nil || !strings.Contains(err.Error(), mode) {
					t.Fatalf("wrong fallback accepted: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("fallback refusal waited", ctx.Err())
			}
			waitSlotServer(t, done)
			if count.Load() != 2 || out.String() != "start "+epochB+":0\n" {
				t.Fatalf("bad fallback output %d %q", count.Load(), out.String())
			}
			// Independently prove the cache publication boundary; the wire gate above
			// also refused every ACK, parameter or other control frame after failure.
			cols := []chunkclient.Column{{Name: "old", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnUnsigned, Size: 8}}}
			schemas := map[watchSchemaKey][]chunkclient.Column{{watchEpoch, 1}: cols}
			meta := map[watchSchemaKey]watchSchemaMetadata{{watchEpoch, 1}: {dims: 2}}
			push := watchPush("change", watchInt("1"), chunkclient.Value{Kind: chunkclient.KindNull}, watchInt("1"), watchArray(watchArray(watchInt("0"), watchInt("0"), chunkclient.Value{Kind: chunkclient.KindNull}, watchArray(watchInt("7")))))
			push.Items[1] = watchBulk(epochB)
			out.Reset()
			err := printWatchEvent(&out, push, false, schemas, func(key watchSchemaKey) (*chunkclient.Schema, error) {
				return &chunkclient.Schema{Epoch: fallbackEpoch, Version: uint64(version), Columns: cols, Dims: 3}, nil
			}, meta)
			if err == nil || out.Len() != 0 || len(schemas) != 1 || len(meta) != 1 {
				t.Fatalf("fallback published: %v %q %v %v", err, out.String(), schemas, meta)
			}
		})
	}
}
func epochColumnValue(name string) chunkclient.Value {
	return foundationMapValue(watchBulk("id"), watchInt("1"), watchBulk("name"), watchBulk(name), watchBulk("type"), watchBulk("u8"), watchBulk("null"), chunkclient.Value{Kind: chunkclient.KindBool}, watchBulk("required"), chunkclient.Value{Kind: chunkclient.KindBool}, watchBulk("default"), chunkclient.Value{Kind: chunkclient.KindNull})
}
func TestWatchEpochSchemaPushesKeepDimensionsAndMetadata(t *testing.T) {
	null := chunkclient.Value{Kind: chunkclient.KindNull}
	schemas := map[watchSchemaKey][]chunkclient.Column{}
	meta := map[watchSchemaKey]watchSchemaMetadata{}
	fetch := func(watchSchemaKey) (*chunkclient.Schema, error) { t.Fatal("owned schema missed"); return nil, nil }
	modern := func(epoch, name, metadata string, dims int) chunkclient.Value {
		return chunkclient.Value{Kind: chunkclient.KindPush, Items: []chunkclient.Value{watchBulk("schema"), foundationMapValue(watchBulk("epoch"), watchBulk(epoch), watchBulk("revision"), watchInt("0"), watchBulk("schema_version"), watchInt("1"), watchBulk("dims"), watchInt(fmt.Sprint(dims)), watchBulk("columns"), watchArray(epochColumnValue(name)), watchBulk("chunk_columns"), watchArray(epochColumnValue(metadata)))}}
	}
	for _, legacyA := range []bool{false, true} {
		schemas = map[watchSchemaKey][]chunkclient.Column{}
		meta = map[watchSchemaKey]watchSchemaMetadata{}
		a := modern(watchEpoch, "old", "old_meta", 2)
		if legacyA {
			a = watchPush("schema", watchInt("1"), watchArray(epochColumnValue("old")))
		}
		for _, s := range []chunkclient.Value{a, modern(epochB, "new", "new_meta", 3)} {
			if err := printWatchEvent(io.Discard, s, true, schemas, fetch, meta); err != nil {
				t.Fatal(err)
			}
		}
		// An unqualified legacy description must not override either exact owner.
		schemas[watchSchemaKey{version: 1}] = []chunkclient.Column{{Name: "unqualified", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnUnsigned, Size: 8}}}
		meta[watchSchemaKey{version: 1}] = watchSchemaMetadata{dims: 2}
		for _, epoch := range []string{watchEpoch, epochB} {
			dims, name, metadata := 2, "old", "old_meta"
			if epoch == epochB {
				dims, name, metadata = 3, "new", "new_meta"
			}
			coordinates := []chunkclient.Value{watchInt("0"), watchInt("0")}
			if dims == 3 {
				coordinates = append(coordinates, watchInt("2"))
			}
			block := append(append([]chunkclient.Value{}, coordinates...), null, watchArray(watchInt("7")))
			changes := watchArray()
			if epoch == epochB || !legacyA {
				entry := append(append([]chunkclient.Value{}, coordinates...), null, watchArray(watchInt("8")))
				changes = watchArray(watchArray(entry...))
			}
			event := watchPush("change", watchInt("1"), null, watchInt("1"), watchArray(watchArray(block...)), null, null, changes)
			event.Items[1] = watchBulk(epoch)
			var out bytes.Buffer
			if err := printWatchEvent(&out, event, true, schemas, fetch, meta); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "\"after\":{\""+name+"\":7}") || strings.Contains(out.String(), "unqualified") {
				t.Fatal("layout alias", out.String())
			}
			if epoch == epochB && !strings.Contains(out.String(), "\"z\":2") {
				t.Fatal("dimensions aliased", out.String())
			}
			if changes.Items != nil && !strings.Contains(out.String(), "\"after\":{\""+metadata+"\":8}") {
				t.Fatal("chunk metadata alias", out.String())
			}
		}
	}
}

func TestWatchEpochLegacyPushUsesParsedPosition(t *testing.T) {
	schemas := map[watchSchemaKey][]chunkclient.Column{}
	meta := map[watchSchemaKey]watchSchemaMetadata{}
	push := watchPush("schema", watchInt("1"), watchArray(epochColumnValue("v")))
	push.Items[1] = watchBulk(strings.ToUpper(watchEpoch))
	if err := printWatchEvent(io.Discard, push, false, schemas, nil, meta); err != nil {
		t.Fatal(err)
	}
	if len(schemas) != 1 || schemas[watchSchemaKey{watchEpoch, 1}][0].Name != "v" || meta[watchSchemaKey{watchEpoch, 1}].dims != 2 {
		t.Fatalf("parsed owner missing: %v %v", schemas, meta)
	}
}
