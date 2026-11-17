package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"reflect"
	"testing"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestCLIChunkModelWatchForms(t *testing.T) {
	for _, dims := range []int{2, 3} {
		for _, tls := range []bool{false, true} {
			for _, chunks := range []bool{false, true} {
				t.Run(fmt.Sprintf("%dD/TLS=%t/CHUNKS=%t", dims, tls, chunks), func(t *testing.T) {
					s := startServerWith(t, tls, false)
					shape, coordinate := "1 x 1", "-2 1"
					if dims == 3 {
						shape, coordinate = "1 x 1 x 1", "-2 1 -3"
					}
					s.ok(t, "CREATE TABLE world (n u8 NULL, unused text(8), label text(8)) CHUNK "+shape+" CHUNK COLUMNS (owner text(8) NULL, counter u64)")
					args := []string{"--json", "--columns", "label,n", "--no-reconnect"}
					kind := "change"
					if chunks {
						args = append(args, "--chunks")
						kind = "chunks"
					}
					p := startWatchProcessTrusted(t, s.uri, s.tlsCert, args...)
					trace := fixtureStart(t, p, true)
					client, err := connect(globalOptions{URI: s.uri, TLSInsecure: tls}, console{})
					if err != nil {
						t.Fatal(err)
					}
					defer client.Close()
					schema, err := client.Describe("world")
					if err != nil {
						t.Fatal(err)
					}
					statements := []string{
						"SET BLOCK " + coordinate + " IN world unused='ignored', n=7, label='chosen'",
						"SET CHUNK " + coordinate + " IN world META owner='named', counter=18446744073709551615",
						"SET CHUNK " + coordinate + " IN world META owner=NULL, counter=0",
						"DELETE BLOCK " + coordinate + " FROM world",
					}
					var revision uint64
					for step, statement := range statements {
						revision = fixtureVersion(t, s.ok(t, statement))
						current := readModelJSON[modelReadChunk](t, s.ok(t, "--json", "GET CHUNK "+coordinate+" FROM world"))
						line, _ := trace.waitMutation(t, p, kind, revision, 1)
						event := readModelJSON[struct {
							CommitTimeMS uint64 `json:"commit_time_ms"`
							Chunks       []struct {
								CX, CY, CZ int64
								Version    uint64
								Form       *string
							}
							Blocks []struct {
								X, Y, Z       int64
								Before, After map[string]any
							}
							ChunkChanges []struct {
								CX, CY, CZ    int64
								Before, After map[string]any
							} `json:"chunk_changes"`
						}](t, line)
						if event.CommitTimeMS == 0 || event.CommitTimeMS != current.CommitTimeMS || current.Version != revision {
							t.Fatalf("producing identity: %s; current %+v", line, current)
						}
						if chunks {
							if len(event.Chunks) != 1 || event.Chunks[0].CX != -2 || event.Chunks[0].CY != 1 || dims == 3 && event.Chunks[0].CZ != -3 || event.Chunks[0].Version != revision {
								t.Fatalf("dimensional chunks: %s", line)
							}
							form := event.Chunks[0].Form
							if step == 3 {
								if form != nil {
									t.Fatalf("deleted form: %s", line)
								}
							} else {
								if form == nil {
									t.Fatalf("missing form: %s", line)
								}
								raw, err := hex.DecodeString(*form)
								if err != nil {
									t.Fatal(err)
								}
								decoded, err := chunkclient.DecodeSectionedChunkForm(schema, []string{"label", "n"}, raw)
								if err != nil || decoded.Version != revision || decoded.CommitTimeMS != current.CommitTimeMS || !decoded.Projected || decoded.PresentCount() != 1 {
									t.Fatalf("projected stream form: %+v, %v", decoded, err)
								}
								var output bytes.Buffer
								if err := printChunk(&output, decoded, -2, 1, statementOptions{json: true, blocks: true}); err != nil {
									t.Fatal(err)
								}
								printed := readModelJSON[modelReadChunk](t, output.String())
								if !reflect.DeepEqual(printed.ChunkColumns, current.ChunkColumns) || len(printed.Blocks) != 1 || !reflect.DeepEqual(printed.Blocks[0].Values, map[string]any{"label": "chosen", "n": float64(7)}) {
									t.Fatalf("stream form values: %s", &output)
								}
							}
						} else if step == 0 || step == 3 {
							if len(event.Blocks) != 1 || event.Blocks[0].X != -2 || event.Blocks[0].Y != 1 || dims == 3 && event.Blocks[0].Z != -3 {
								t.Fatalf("dimensional blocks: %s", line)
							}
							row := event.Blocks[0]
							values := map[string]any{"label": "chosen", "n": float64(7)}
							if step == 0 && (row.Before != nil || !reflect.DeepEqual(row.After, values)) || step == 3 && (!reflect.DeepEqual(row.Before, values) || row.After != nil) {
								t.Fatalf("projected before/after rows: %s", line)
							}
						} else if len(event.Blocks) != 0 || len(event.ChunkChanges) != 1 || event.ChunkChanges[0].CX != -2 || event.ChunkChanges[0].CY != 1 || dims == 3 && event.ChunkChanges[0].CZ != -3 || !reflect.DeepEqual(event.ChunkChanges[0].After, current.ChunkColumns) {
							t.Fatalf("metadata-only change: %s", line)
						}
					}
					s.ok(t, "ALTER TABLE world RENAME COLUMN label TO caption")
					revision = fixtureTableFrontier(t, s)
					trace.waitSchema(t, p, revision, 2)
					type namedColumn struct {
						ID   uint32
						Name string
					}
					changed := readModelJSON[struct {
						Dims         int
						Columns      []namedColumn
						ChunkColumns []namedColumn `json:"chunk_columns"`
					}](t, trace.raw[len(trace.raw)-1])
					if changed.Dims != dims || !reflect.DeepEqual(changed.Columns, []namedColumn{{3, "caption"}, {1, "n"}}) || !reflect.DeepEqual(changed.ChunkColumns, []namedColumn{{1, "owner"}, {2, "counter"}}) {
						t.Fatalf("projected dimensional schema: %+v", changed)
					}
					trace.stop(t, p, revision, 2)
				})
			}
		}
	}
}
