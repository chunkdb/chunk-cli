package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestFoundationJSONIntegerRange(t *testing.T) {
	for _, tc := range []struct{ text, want string }{
		{"-9007199254740992", `"-9007199254740992"`}, {"-9007199254740991", "-9007199254740991"}, {"0", "0"}, {"9007199254740991", "9007199254740991"}, {"9007199254740992", `"9007199254740992"`}, {"18446744073709551615", `"18446744073709551615"`}, {"170141183460469231731687303715884105727", `"170141183460469231731687303715884105727"`},
	} {
		v := chunkclient.Value{Kind: chunkclient.KindBigNumber, Text: tc.text}
		var out bytes.Buffer
		if err := printReply(&out, v, statementOptions{json: true}); err != nil || out.String() != tc.want+"\n" {
			t.Fatal(tc, out.String(), err)
		}
	}
	var out bytes.Buffer
	if err := writeJSON(&out, orderedMap{{"position", watchPosition{watchEpoch, ^uint64(0)}}, {"coords", []any{int64(-9007199254740992), uint64(9007199254740991)}}, {"floating", json.Number("9007199254740992.0")}}); err != nil {
		t.Fatal(err)
	}
	want := `{"position":{"epoch":"` + watchEpoch + `","revision":"18446744073709551615"},"coords":["-9007199254740992",9007199254740991],"floating":9007199254740992.0}` + "\n"
	if out.String() != want {
		t.Fatal(out.String())
	}
}

func TestFoundationCLIErrorExitCodes(t *testing.T) {
	for code, exit := range map[string]int{"VERSION_MISMATCH": 2, "CONDITION_FAILED": 2, "SCHEMA_MISMATCH": 2, "CONFLICT": 3, "BUSY": 4, "DISK_FULL": 4, "RATE_LIMITED": 4, "OUTCOME_UNKNOWN": 5, "SLOT_BUSY": 1, "MIGRATION_MISMATCH": 1, "CANCELLED": 1, "TIMEOUT": 1, "KILLED": 1, "FUTURE_ERROR": 1} {
		t.Run(code, func(t *testing.T) {
			uri, done := fakeMigrationServer(t, []string{"SHOW TABLES"}, []string{"-ERR " + code + " current=18446744073709551615 future=a%20b%3Dc malformed=%Q0 backoff CONFLICT\r\n"})
			var out, failure bytes.Buffer
			if got := run([]string{"--uri", uri, "--json", "SHOW TABLES"}, strings.NewReader(""), &out, &failure); got != exit || out.Len() != 0 {
				t.Fatal(got, out.String(), failure.String())
			}
			var envelope struct {
				Error struct {
					Code, Class, Message string
					Keys                 map[string]any
				}
			}
			if err := json.Unmarshal(failure.Bytes(), &envelope); err != nil {
				t.Fatal(err, failure.String())
			}
			if envelope.Error.Code != code || envelope.Error.Class != (&chunkclient.ServerError{Code: code}).RetryClass() || envelope.Error.Message != "backoff CONFLICT" || envelope.Error.Keys["current"] != "18446744073709551615" || envelope.Error.Keys["future"] != "a b=c" || envelope.Error.Keys["malformed"] != "%Q0" {
				t.Fatal(failure.String())
			}
			waitSlotServer(t, done)
		})
	}
}

func foundationDescribeWire() string {
	return watchWireMap("table", watchWireBulk("world"), "version", ":1\r\n", "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", "%0\r\n", "columns", "*1\r\n"+slotWireColumn(), "epoch", watchWireBulk(watchEpoch), "revision", "_\r\n", "dims", ":2\r\n", "chunk_columns", "*0\r\n", "future", "*1\r\n_\r\n")
}

func TestFoundationReadMetadataWire(t *testing.T) {
	for _, tc := range []struct {
		statement, reply, want string
		exit                   int
	}{
		{"GET BLOCK 0 0 FROM world WITH VERSION WITH SCHEMA", "*4\r\n_\r\n:1\r\n_\r\n%0\r\n", `{"version":null,"schema_version":1,"values":null}`, 0},
		{"GET BLOCK 0 0 FROM world COLUMNS v WITH SCHEMA WITH VERSION", "*3\r\n(18446744073709551615\r\n:1\r\n*2\r\n:7\r\n$9\r\nextension\r\n", `{"version":"18446744073709551615","schema_version":1,"values":{"v":7}}`, 0},
		{"GET AREA 0 0 TO 0 0 FROM world WITH SCHEMA", "*2\r\n:1\r\n*0\r\n", `{"schema_version":1,"entries":[]}`, 0},
		{"GET BLOCK 0 0 FROM world WITH SCHEMA", "*2\r\n:2\r\n*1\r\n:7\r\n", "", 2},
	} {
		t.Run(tc.statement, func(t *testing.T) {
			uri, done := fakeMigrationServer(t, []string{"DESCRIBE world", tc.statement}, []string{foundationDescribeWire(), tc.reply})
			var out, failure bytes.Buffer
			got := run([]string{"--uri", uri, "--json", tc.statement}, strings.NewReader(""), &out, &failure)
			if got != tc.exit || tc.want != "" && out.String() != tc.want+"\n" || tc.exit != 0 && (!strings.Contains(failure.String(), `"code":"SCHEMA_MISMATCH"`) || out.Len() != 0) {
				t.Fatal(got, out.String(), failure.String())
			}
			waitSlotServer(t, done)
		})
	}
	if got := classify("GET BLOCK 0 0"); got.kind != statementOther || got.table != "" {
		t.Fatal("implicit table", got)
	}
}

func foundationSchemaPush() string {
	return ">2\r\n" + watchWireBulk("schema") + watchWireMap("epoch", watchWireBulk(watchEpoch), "revision", ":1\r\n", "schema_version", ":1\r\n", "dims", ":2\r\n", "columns", "*1\r\n"+slotWireColumn(), "chunk_columns", "*0\r\n", "future", "%0\r\n")
}

func foundationChunksPush(revision int) string {
	return ">10\r\n" + watchWireBulk("chunks") + watchWireBulk(watchEpoch) + fmt.Sprintf(":%d\r\n(18446744073709551615\r\n_\r\n:1\r\n*1\r\n*5\r\n:-2\r\n:3\r\n(18446744073709551615\r\n$2\r\nxy\r\n_\r\n_\r\n(18446744073709551615\r\n%%0\r\n", revision)
}

func TestFoundationWatchACKWire(t *testing.T) {
	uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
		prefix := watchWireMap("epoch", watchWireBulk(watchEpoch), "revision", ":0\r\n") + foundationSchemaPush() + ">2\r\n" + watchWireBulk("future") + "*1\r\n(18446744073709551616\r\n" + ">7\r\n" + watchWireBulk("catalog") + watchWireBulk(watchEpoch) + ":5\r\n:1\r\n_\r\n*0\r\n_\r\n" + ">5\r\n" + watchWireBulk("resync") + watchWireBulk(watchEpoch) + ":6\r\n_\r\n_\r\n"
		if _, err := io.WriteString(conn, prefix+foundationChunksPush(9)); err != nil {
			return err
		}
		if err := expectSlotLine(r, "ACK 9"); err != nil {
			return err
		}
		if _, err := io.WriteString(conn, ">4\r\n"+watchWireBulk("progress")+watchWireBulk(watchEpoch)+":11\r\n_\r\n"); err != nil {
			return err
		}
		return expectSlotLine(r, "ACK 11")
	})
	var out bytes.Buffer
	err := runWatch(t.Context(), globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 1, json: true}, console{}, &out)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "future") || !strings.Contains(out.String(), `"txn":"18446744073709551615"`) || !strings.Contains(out.String(), `"version":"18446744073709551615"`) || !strings.Contains(out.String(), `"chunk_columns":[]`) || !strings.Contains(out.String(), `"type":"progress"`) {
		t.Fatal(out.String())
	}
	waitSlotServer(t, done)
}

func TestFoundationChunkOutputFailureDoesNotACK(t *testing.T) {
	for _, jsonOutput := range []bool{false, true} {
		for _, short := range []bool{false, true} {
			t.Run(fmt.Sprintf("json=%t/short=%t", jsonOutput, short), func(t *testing.T) {
				uri, done := fakeSlotServer(t, func(conn net.Conn, r *bufio.Reader) error {
					if _, err := io.WriteString(conn, "+OK "+watchEpoch+" 0\r\n"+foundationSchemaPush()+foundationChunksPush(9)); err != nil {
						return err
					}
					line, err := r.ReadString('\n')
					if line != "" || !errors.Is(err, io.EOF) {
						return fmt.Errorf("ACK after failed output: %q %v", line, err)
					}
					return nil
				})
				err := runWatch(context.Background(), globalOptions{URI: uri, Timeout: time.Second}, watchOptions{table: "world", statement: "WATCH world SLOT 'consumer'", slot: "consumer", ackEvery: 1, json: jsonOutput}, console{}, &slotOutput{failType: "chunks", short: short})
				if err == nil || short && !errors.Is(err, io.ErrShortWrite) || !short && !strings.Contains(err.Error(), "output failed") {
					t.Fatal(err)
				}
				waitSlotServer(t, done)
			})
		}
	}
}

func foundationMapValue(pairs ...chunkclient.Value) chunkclient.Value {
	result := chunkclient.Value{Kind: chunkclient.KindMap}
	for i := 0; i < len(pairs); i += 2 {
		result.Map = append(result.Map, chunkclient.MapEntry{Key: pairs[i], Value: pairs[i+1]})
	}
	return result
}

func TestFoundationWatchNestedExtensionsAndChunkMetadata(t *testing.T) {
	null := chunkclient.Value{Kind: chunkclient.KindNull}
	column := foundationMapValue(watchBulk("id"), watchInt("1"), watchBulk("name"), watchBulk("value"), watchBulk("type"), watchBulk("decimal(20)"), watchBulk("null"), chunkclient.Value{Kind: chunkclient.KindBool}, watchBulk("required"), chunkclient.Value{Kind: chunkclient.KindBool}, watchBulk("default"), null, watchBulk("future"), watchArray(null))
	schema := chunkclient.Value{Kind: chunkclient.KindPush, Items: []chunkclient.Value{watchBulk("schema"), foundationMapValue(watchBulk("epoch"), watchBulk(watchEpoch), watchBulk("revision"), watchInt("9"), watchBulk("schema_version"), watchInt("1"), watchBulk("dims"), watchInt("3"), watchBulk("columns"), watchArray(column), watchBulk("chunk_columns"), watchArray(column), watchBulk("future"), watchArray(null)), null}}
	schemas := map[uint64][]chunkclient.Column{}
	metadata := map[uint64]watchSchemaMetadata{}
	fetch := func(uint64) ([]chunkclient.Column, error) { t.Fatal("schema push not cached"); return nil, nil }
	var out bytes.Buffer
	if err := printWatchEvent(&out, schema, true, schemas, fetch, metadata); err != nil || !strings.Contains(out.String(), `"dims":3`) || !strings.Contains(out.String(), `"chunk_columns":[{"id":1,"name":"value","type":"decimal(20)"`) {
		t.Fatal(err, out.String())
	}
	block := watchArray(watchArray(watchInt("-9007199254740992"), watchInt("1"), null), watchInt("0"), watchInt("2"), null, watchArray(watchBulk("raw"), null), null)
	chunk := watchArray(watchInt("-2"), watchInt("0"), watchInt("2"), null, watchArray(watchBulk("meta"), null), null)
	event := watchPush("change", watchInt("1"), null, watchInt("1"), watchArray(block), watchBulk("\x00\xff"), chunkclient.Value{Kind: chunkclient.KindBigNumber, Text: "18446744073709551615"}, watchArray(chunk), null)
	out.Reset()
	if err := printWatchEvent(&out, event, true, schemas, fetch, metadata); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"x":["-9007199254740992",1],"y":0,"z":2`, `"after":{"value":"726177"}`, `"chunk_changes":[{"cx":-2,"cy":0,"cz":2,"before":null,"after":{"value":"6d657461"}}]`, `"tag":"00ff","txn":"18446744073709551615"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, out.String())
		}
	}
	out.Reset()
	if err := printWatchEvent(&out, event, false, schemas, fetch, metadata); err != nil || !strings.Contains(out.String(), "chunk metadata -2 0 2: (absent) -> {value = x'6d657461'}") {
		t.Fatal(err, out.String())
	}
	bad := event
	bad.Items = append([]chunkclient.Value{}, event.Items...)
	bad.Items[8] = chunkclient.Value{Kind: chunkclient.KindBigNumber, Text: "18446744073709551616"}
	out.Reset()
	if err := printWatchEvent(&out, bad, true, schemas, fetch, metadata); err == nil || out.Len() != 0 {
		t.Fatal("invalid transaction id produced output", err, out.String())
	}
}

func TestFoundationJSONFlagError(t *testing.T) {
	var out, failure bytes.Buffer
	if code := run([]string{"--json", "--hello-option", "statement_timeout_ms=-1", "PING"}, strings.NewReader(""), &out, &failure); code != 1 || !json.Valid(failure.Bytes()) || !strings.Contains(failure.String(), `"code":"CLIENT_ERROR"`) {
		t.Fatal(code, out.String(), failure.String())
	}
}

func TestFoundationReservedCatalogFields(t *testing.T) {
	null := chunkclient.Value{Kind: chunkclient.KindNull}
	event := foundationMapValue(watchBulk("kind"), watchBulk("future"), watchBulk("action"), null, watchBulk("name"), null, watchBulk("table"), null, watchBulk("data"), foundationMapValue(watchBulk("future"), watchInt("9007199254740992")), watchBulk("extension"), watchArray(null))
	push := watchPush("catalog", watchInt("1"), null, watchArray(event), null, null)
	var out bytes.Buffer
	if err := printWatchEvent(&out, push, true, nil, nil); err != nil || !strings.Contains(out.String(), `"future":"9007199254740992"`) {
		t.Fatal(out.String(), err)
	}
	broken := event
	broken.Map = append([]chunkclient.MapEntry{}, event.Map...)
	broken.Map[0].Value = watchInt("1")
	push.Items[5] = watchArray(broken)
	out.Reset()
	if err := printWatchEvent(&out, push, true, nil, nil); err == nil || out.Len() != 0 {
		t.Fatal("invalid catalog field printed", err, out.String())
	}
}

func TestFoundationWatchFlags(t *testing.T) {
	opts, err := parseWatchArgs([]string{"world", "--slot", "consumer", "--columns", "v", "--no-before", "--chunks", "--takeover", "--ack-timeout-ms", "500"}, false)
	if err != nil || opts.statement != "WATCH world SLOT 'consumer' COLUMNS v NO BEFORE CHUNKS TAKEOVER ACK TIMEOUT 500" {
		t.Fatal(opts, err)
	}
	for _, args := range [][]string{{"world", "--takeover"}, {"world", "--ack-timeout-ms", "1"}, {"world", "--slot", "consumer", "--ack-timeout-ms", "0"}, {"world", "--columns", "v,$1"}} {
		if _, err := parseWatchArgs(args, false); err == nil {
			t.Fatal("accepted", args)
		}
	}
}
