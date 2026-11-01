package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

// These tests require the complete A1–A9/G7 server, rather than an older
// production binary that happens to pass the existing integration tests.
func TestFoundationServerContract(t *testing.T) {
	for _, tls := range []bool{false, true} {
		t.Run(fmt.Sprintf("TLS=%t", tls), func(t *testing.T) {
			s := startServer(t, tls)
			uri, err := chunkuri.Parse(s.uri)
			if err != nil {
				t.Fatal(err)
			}
			client, err := chunkclient.Dial(chunkclient.Config{URI: uri, Timeout: 5 * time.Second, TLSInsecure: tls, SessionOptions: map[string]chunkclient.Value{
				"client_name": {Kind: chunkclient.KindBulk, Bulk: []byte("foundation-cli")}, "statement_timeout_ms": {Kind: chunkclient.KindInteger, Text: "1000"}, "future_option": {Kind: chunkclient.KindBulk, Bulk: []byte("future")},
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			info, err := client.Hello(chunkclient.Login{User: adminUser, Password: adminPassword})
			if err != nil || !info.Supports("protocol_foundation") || !info.Supports("read_metadata") || info.ConnectionID == 0 || string(info.Options["client_name"].Bulk) != "foundation-cli" || info.NotApplied["future_option"] != "unknown_option" || len(info.Limits) != 7 {
				t.Fatalf("HELLO: %+v %v", info, err)
			}
			if info.Supports("statement_timeout") {
				if timeout, err := info.Options["statement_timeout_ms"].Uint64(); err != nil || timeout != 1000 {
					t.Fatal(info.Options, err)
				}
			} else if info.NotApplied["statement_timeout_ms"] != "implementation_pending" {
				t.Fatal(info.NotApplied)
			}
			status, err := client.Do("SHOW STATUS")
			if err != nil {
				t.Fatal(err)
			}
			if err := chunkclient.ValidateStatus(status); err != nil {
				t.Fatal(err)
			}
			s.ok(t, "CREATE TABLE world (id u64) CHUNK 2 x 2")
			schema, err := client.Describe("world")
			if err != nil || schema.Revision == nil || schema.Dims != 2 || !chunkclient.ValidEpoch(schema.Epoch) {
				t.Fatalf("DESCRIBE: %+v %v", schema, err)
			}
			if out := s.ok(t, "--json", "GET BLOCK 0 0 FROM world WITH SCHEMA WITH VERSION"); out != "{\"version\":null,\"schema_version\":1,\"values\":null}\n" {
				t.Fatal(out)
			}
			version := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 18446744073709551615"))
			if out := s.ok(t, "--json", "GET BLOCK 0 0 FROM world WITH VERSION WITH SCHEMA"); out != "{\"version\":"+version+",\"schema_version\":1,\"values\":{\"id\":\"18446744073709551615\"}}\n" {
				t.Fatal(out)
			}
			if out := s.ok(t, "--json", "GET AREA 4 4 TO 4 4 FROM world WITH SCHEMA"); out != "{\"schema_version\":1,\"entries\":[]}\n" {
				t.Fatal(out)
			}
			code, out, failure := s.cli(t, "", "--json", "SET BLOCK 0 0 IN world id = 7 IF VERSION 0")
			if code != 2 || out != "" || !strings.Contains(failure, `"class":"reread"`) || !strings.Contains(failure, `"code":"VERSION_MISMATCH"`) {
				t.Fatal(code, out, failure)
			}
			s.ok(t, "MIGRATE 'foundation_step' CREATE TABLE migration_example (id u8) CHUNK 2 x 2")
			code, out, failure = s.cli(t, "", "--json", "MIGRATE 'foundation_step' CREATE TABLE migration_example (id u16) CHUNK 2 x 2")
			assertFoundationFatalError(t, code, out, failure, "MIGRATION_MISMATCH", "migration_mismatch")
			s.ok(t, "CREATE SLOT 'consumer' ON world AREA 0 0 TO 0 0 COLUMNS id NO BEFORE CHUNKS")
			slots, err := client.Do("SHOW SLOTS ON world")
			if err != nil {
				t.Fatal(err)
			}
			if err := chunkclient.ValidateSlots(slots); err != nil {
				t.Fatal(err)
			}
			filters, _ := slots.Items[0].Lookup("filters")
			chunks, _ := filters.Lookup("chunks")
			before, _ := filters.Lookup("no_before")
			if !chunks.Bool || !before.Bool {
				t.Fatal(filters)
			}
			watchURI, proxy := watchProxy(t, s)
			p := startWatchProcessTrusted(t, watchURI, s.tlsCert, "--slot", "consumer", "--json")
			trace := fixtureStart(t, p, true)
			code, out, failure = s.cli(t, "", "--json", "watch", "world", "--slot", "consumer")
			assertFoundationFatalError(t, code, out, failure, "SLOT_BUSY", "slot_attached")
			written := strings.TrimSpace(s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
			revision := fixtureVersion(t, written)
			line, _ := trace.waitMutation(t, p, "chunks", revision, 1)
			if !strings.Contains(line, `"version":`+written) || !strings.Contains(line, `"chunks":[{"cx":0,"cy":0,`) || !strings.Contains(line, `"tag":null,"txn":null`) {
				t.Fatal(line)
			}
			trace.awaitACK(t, proxy, p.lines, revision, 1)
			trace.stop(t, p, revision, 1)
			if got := fmt.Sprint(slotAcked(t, s)); got != written {
				t.Fatal("ACK frontier", got, written)
			}
		})
	}
}

func assertFoundationFatalError(t *testing.T, code int, out, failure, expected, reason string) {
	t.Helper()
	var response struct {
		Error struct {
			Code, Class string
			Keys        map[string]string
		}
	}
	if err := json.Unmarshal([]byte(failure), &response); err != nil || code != 1 || out != "" || response.Error.Code != expected || response.Error.Class != "fatal" || response.Error.Keys["reason"] != reason {
		t.Fatalf("%s must preserve fatal code and metadata: exit %d stdout %q stderr %q", expected, code, out, failure)
	}
	t.Logf("Native fatal error: %s", strings.TrimSpace(failure))
}
