package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func modelScanFixture(t *testing.T) (*chunkclient.Schema, chunkclient.Value) {
	t.Helper()
	chunk := outputModelChunk(t)
	schema := &chunkclient.Schema{Version: 7, Epoch: strings.Repeat("a", 32), Dims: 3, ChunkWidth: 2, ChunkHeight: 1, ChunkDepth: 1, Columns: chunk.Columns, ChunkColumns: chunk.MetadataColumns}
	integer := func(n string) chunkclient.Value { return chunkclient.Value{Kind: chunkclient.KindInteger, Text: n} }
	reply := chunkclient.Value{Kind: chunkclient.KindMap}
	for _, pair := range []struct {
		key   string
		value chunkclient.Value
	}{
		{"chunks", chunkclient.Value{Kind: chunkclient.KindArray, Items: []chunkclient.Value{
			{Kind: chunkclient.KindArray, Items: []chunkclient.Value{integer("-1"), integer("2"), integer("0"), integer("9"), {Kind: chunkclient.KindBulk, Bulk: outputModelForm(t)}}},
			{Kind: chunkclient.KindArray, Items: []chunkclient.Value{integer("0"), integer("0"), integer("-1"), integer("18446744073709551615"), {Kind: chunkclient.KindNull}}},
		}}},
		{"more", chunkclient.Value{Kind: chunkclient.KindBool}},
		{"epoch", chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte(schema.Epoch)}},
		{"position", chunkclient.Value{Kind: chunkclient.KindNull}},
		{"retained_deletion_floor", integer("3")},
	} {
		reply.Map = append(reply.Map, chunkclient.MapEntry{Key: chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte(pair.key)}, Value: pair.value})
	}
	return schema, reply
}

func TestChunkModelScanOutput(t *testing.T) {
	schema, reply := modelScanFixture(t)
	info := classify(`SCAN CHUNKS FROM world AFTER -2 0 0 SINCE 3 LIMIT 4 WITH DATA`)
	if info.kind != statementScanChunks || info.scanSince == nil || *info.scanSince != 3 {
		t.Fatal(info)
	}
	var out bytes.Buffer
	if err := printChunkScan(&out, schema, info, reply, statementOptions{blocks: true, json: true}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"cx":-1,"cy":2,"cz":0`, `"chunk_columns":{"flag":true,"label":null}`, `"z":0`, `"version":"18446744073709551615","deleted":true,"chunk":null`, `"position":null`, `"retained_deletion_floor":3`} {
		if !strings.Contains(out.String(), want) {
			t.Fatal(want, out.String())
		}
	}
	out.Reset()
	if err := printChunkScan(&out, schema, info, reply, statementOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "chunk -1 2 0\n") || !strings.Contains(out.String(), "chunk 0 0 -1\n  version = 18446744073709551615\n  deleted = true\n") {
		t.Fatal(out.String())
	}
}

func TestChunkModelScanRejectsInvalidIdentity(t *testing.T) {
	for _, mode := range []string{"ordinary tombstone", "version", "zero version", "epoch", "floor", "order", "more empty", "frontier"} {
		t.Run(mode, func(t *testing.T) {
			schema, reply := modelScanFixture(t)
			info := classify(`SCAN CHUNKS FROM world SINCE 3 WITH DATA`)
			switch mode {
			case "ordinary tombstone":
				info.scanSince = nil
			case "version":
				reply.Map[0].Value.Items[0].Items[3].Text = "8"
			case "zero version":
				reply.Map[0].Value.Items[0].Items[3].Text = "0"
			case "epoch":
				reply.Map[2].Value.Bulk = []byte(strings.Repeat("b", 32))
			case "floor":
				reply.Map[4].Value.Text = "4"
			case "order":
				reply.Map[0].Value.Items[1].Items[0].Text = "-2"
			case "more empty":
				reply.Map[0].Value.Items = nil
				reply.Map[1].Value.Bool = true
			case "frontier":
				reply.Map[3].Value = chunkclient.Value{Kind: chunkclient.KindInteger, Text: "-1"}
			}
			var out bytes.Buffer
			if err := printChunkScan(&out, schema, info, reply, statementOptions{json: true}); err == nil || out.Len() != 0 {
				t.Fatal("invalid page emitted", err, out.String())
			}
		})
	}
}

func TestFilteredScanReadsAfter(t *testing.T) {
	schema, reply := modelScanFixture(t)
	info := classify(`SCAN CHUNKS FROM world LIMIT 4 WHERE kind > 2 AND POS_X < 9 WITH DATA COLUMNS kind`)
	if info.kind != statementScanChunks || !info.scanWhere || len(info.columns) != 1 {
		t.Fatal(info)
	}
	if classify(`SCAN CHUNKS FROM world WHERE kind > 2`).kind == statementScanChunks {
		t.Fatal("a scan without data took the data renderer")
	}
	empty := chunkclient.Value{Kind: chunkclient.KindArray}
	set := func(key string, value chunkclient.Value) chunkclient.Value {
		out := chunkclient.Value{Kind: chunkclient.KindMap}
		found := false
		for _, entry := range reply.Map {
			if string(entry.Key.Bulk) == key {
				entry.Value, found = value, true
			}
			out.Map = append(out.Map, entry)
		}
		if !found {
			out.Map = append(out.Map, chunkclient.MapEntry{Key: chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte(key)}, Value: value})
		}
		return out
	}
	integer := func(n string) chunkclient.Value { return chunkclient.Value{Kind: chunkclient.KindInteger, Text: n} }
	point := chunkclient.Value{Kind: chunkclient.KindArray, Items: []chunkclient.Value{integer("1"), integer("2"), integer("0")}}
	reply = set("chunks", empty)
	reply = set("more", chunkclient.Value{Kind: chunkclient.KindBool, Bool: true})
	page := set("after", point)
	var out bytes.Buffer
	if err := printChunkScan(&out, schema, info, page, statementOptions{json: true}); err != nil || !strings.Contains(out.String(), `"chunks":[],"more":true,"after":[1,2,0]`) {
		t.Fatal(out.String(), err)
	}
	out.Reset()
	if err := printChunkScan(&out, schema, info, page, statementOptions{}); err != nil || !strings.Contains(out.String(), "after = 1 2 0\n") {
		t.Fatal(out.String(), err)
	}
	for _, broken := range []chunkclient.Value{reply, set("after", chunkclient.Value{Kind: chunkclient.KindNull}), set("after", empty)} {
		if err := printChunkScan(&out, schema, info, broken, statementOptions{}); err == nil {
			t.Fatal("accepted a broken filtered page")
		}
	}
}
