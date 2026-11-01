package main

import (
	"bytes"
	"encoding/hex"
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestChunkModelExplicitThirdAxisOutput(t *testing.T) {
	for _, z := range []int64{0, -3} {
		chunk := outputModelChunk(t)
		var out bytes.Buffer
		if err := printChunk(&out, chunk, -1, 2, statementOptions{blocks: true}, z); err != nil {
			t.Fatal(err)
		}
		want := "block -1 2 " + strconv.FormatInt(z, 10) + "\n"
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
		out.Reset()
		if err := printChunk(&out, chunk, -1, 2, statementOptions{blocks: true, json: true}, z); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"z":`+strconv.FormatInt(z, 10)) {
			t.Fatal(out.String())
		}
	}
	for statement, want := range map[string]statementInfo{
		`GET CHUNK -1 2 0 FROM world COLUMNS "with", "a""b"`: {kind: statementGetChunk, table: "world", columns: []string{"with", `a"b`}, cx: -1, cy: 2, dims: 3},
		`GET BLOCK 0 0 -1 FROM world WITH VERSION`:           {kind: statementGetBlock, table: "world", cz: -1, dims: 3, withVersion: true},
		`GET CHUNK 0 0 0 0 FROM world`:                       {},
	} {
		if got := classify(statement); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %+v want %+v", statement, got, want)
		}
	}
}

func outputModelChunk(t *testing.T) *chunkclient.Chunk {
	t.Helper()
	data, err := hex.DecodeString("434b434601000000090000000000000007000000000000000b000000000000000500000001000000600000000100000002000000610000000200000004000000630000000c000000050000006f0000002500000006000000940000001c0000000200070200000001000000000000000200000001000000000000002400000001000000020000000100000025000000000000000101000000010000000800000000000000020000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	schema := &chunkclient.Schema{Version: 7, Dims: 3, ChunkWidth: 2, ChunkHeight: 1, ChunkDepth: 1,
		Columns:      []chunkclient.Column{{ID: 1, Name: "n", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnUnsigned, Size: 8}}, {ID: 2, Name: "text", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnText, Size: 8}, Null: true}},
		ChunkColumns: []chunkclient.Column{{ID: 1, Name: "flag", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnBool, Size: 1}}, {ID: 2, Name: "label", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnText, Size: 8}, Null: true}}}
	chunk, err := chunkclient.DecodeSectionedChunkForm(schema, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	return chunk
}

func TestChunkModelMetadataOutput(t *testing.T) {
	chunk := outputModelChunk(t)
	var out bytes.Buffer
	if err := printChunk(&out, chunk, 0, 0, statementOptions{blocks: true}); err != nil {
		t.Fatal(err)
	}
	want := "version = 9\nschema_version = 7\ncommit_time_ms = 11\npresent = 1 of 2 blocks\nchunk_columns:\n  flag = true\n  label = NULL\nblock 1 0\n  n = 7\n  text = ''\n"
	if out.String() != want {
		t.Fatalf("want %q got %q", want, out.String())
	}
	out.Reset()
	if err := printChunk(&out, chunk, 0, 0, statementOptions{json: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"commit_time_ms":11`) || !strings.Contains(out.String(), `"chunk_columns":{"flag":true,"label":null}`) {
		t.Fatal(out.String())
	}
	chunk.Private = true
	chunk.Version = 0
	chunk.CommitTimeMS = 0
	out.Reset()
	if err := printChunk(&out, chunk, 0, 0, statementOptions{json: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"version":null`) || !strings.Contains(out.String(), `"commit_time_ms":null`) || !strings.Contains(out.String(), `"private":true`) {
		t.Fatal(out.String())
	}
}

func TestChunkModelWorldCoordinateOverflowOutput(t *testing.T) {
	chunk := &chunkclient.Chunk{Width: 2, Height: 2, Present: []bool{false, true, false, false}, Columns: []chunkclient.Column{{Name: "n", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnUnsigned, Size: 8}}}, Values: [][]chunkclient.Value{{{Kind: chunkclient.KindNull}, {Kind: chunkclient.KindInteger, Text: "7"}, {Kind: chunkclient.KindNull}, {Kind: chunkclient.KindNull}}}}
	var out bytes.Buffer
	if err := printChunk(&out, chunk, math.MaxInt64, 0, statementOptions{blocks: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "block [9223372036854775807,1] 0\n") {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := printChunk(&out, chunk, math.MaxInt64, 0, statementOptions{blocks: true, json: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"x":["9223372036854775807",1]`) {
		t.Fatal(out.String())
	}
}
