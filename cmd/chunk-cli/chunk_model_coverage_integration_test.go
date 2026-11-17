package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

type modelReadChunk struct {
	CX, CY, CZ    int64
	Version       uint64
	SchemaVersion uint64 `json:"schema_version"`
	CommitTimeMS  uint64 `json:"commit_time_ms"`
	Present       int
	BlockCount    int `json:"block_count"`
	Projected     bool
	ChunkColumns  map[string]any `json:"chunk_columns"`
	Blocks        []struct {
		X, Y, Z int64
		Values  map[string]any
	}
}

func readModelJSON[T any](t *testing.T, output string) T {
	t.Helper()
	var result T
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		t.Fatalf("decode %q: %v", output, err)
	}
	return result
}

func TestCLIChunkModelReadForms(t *testing.T) {
	for _, dims := range []int{2, 3} {
		for _, tls := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dD/TLS=%t", dims, tls), func(t *testing.T) {
				s := startServerWith(t, tls, false)
				shape, first, last, block, missing := "2 x 2", "-1 0", "0 0", "-1 1", "5 5"
				if dims == 3 {
					shape, first, last, block, missing = "2 x 2 x 2", "-1 0 -1", "0 0 0", "-1 1 -1", "5 5 5"
				}
				s.ok(t, "CREATE TABLE model (n u8 NULL, label text(8) NULL, raw bytes(8), flag bool, ratio f64) CHUNK "+shape+
					" CHUNK COLUMNS (tiny u3, wide u64, signed i64, small f32, real f64, flag bool, mask bits(9), label text(32), raw bytes(8), n u8 NULL, empty text(8) NULL, empty_raw bytes(8) NULL)")
				if out := s.ok(t, "--json", "GET CHUNK "+missing+" FROM model"); out != "null\n" {
					t.Fatalf("unwritten chunk: %q", out)
				}
				metadata := map[string]any{"tiny": float64(5), "wide": "18446744073709551615", "signed": "-9223372036854775808",
					"small": 1.5, "real": -2.25, "flag": true, "mask": "101010101", "label": "snow ☃", "raw": "00ff0d0a", "n": nil, "empty": "", "empty_raw": ""}
				s.ok(t, "SET CHUNK "+first+" IN model META tiny=5, wide=18446744073709551615, signed=-9223372036854775808, small=1.5, real=-2.25, flag=TRUE, mask=b'101010101', label='snow ☃', raw=x'00ff0d0a', n=NULL, empty='', empty_raw=x''")
				get := "GET CHUNK " + first + " FROM model"
				metaOnly := readModelJSON[modelReadChunk](t, s.ok(t, "--json", "--blocks", get))
				if metaOnly.SchemaVersion != 1 || metaOnly.BlockCount != 1<<dims || metaOnly.Present != 0 || len(metaOnly.Blocks) != 0 || !reflect.DeepEqual(metaOnly.ChunkColumns, metadata) || metaOnly.CommitTimeMS == 0 {
					t.Fatalf("metadata-only chunk: %+v", metaOnly)
				}
				s.ok(t, "SET BLOCK "+block+" IN model n=NULL, label='', raw=x'00ff', flag=TRUE, ratio=3.5")
				full := readModelJSON[modelReadChunk](t, s.ok(t, "--json", "--blocks", get))
				if full.Version <= metaOnly.Version || full.CommitTimeMS < metaOnly.CommitTimeMS || full.Present != 1 || len(full.Blocks) != 1 || !reflect.DeepEqual(full.ChunkColumns, metadata) {
					t.Fatalf("full chunk: %+v", full)
				}
				values := map[string]any{"n": nil, "label": "", "raw": "00ff", "flag": true, "ratio": 3.5}
				if full.Blocks[0].X != -1 || full.Blocks[0].Y != 1 || dims == 3 && full.Blocks[0].Z != -1 || !reflect.DeepEqual(full.Blocks[0].Values, values) {
					t.Fatalf("world block coordinates or values: %+v", full.Blocks)
				}
				for _, flags := range [][]string{{"--json"}, {"--json", "--blocks"}} {
					projected := readModelJSON[modelReadChunk](t, s.ok(t, append(flags, get+" COLUMNS raw,n")...))
					if !projected.Projected || projected.Version != full.Version || projected.CommitTimeMS != full.CommitTimeMS || !reflect.DeepEqual(projected.ChunkColumns, metadata) {
						t.Fatalf("projected metadata or identity: %+v", projected)
					}
					if len(flags) == 2 && (len(projected.Blocks) != 1 || !reflect.DeepEqual(projected.Blocks[0].Values, map[string]any{"raw": "00ff", "n": nil})) {
						t.Fatalf("projection: %+v", projected.Blocks)
					}
				}
				s.ok(t, "SET CHUNK "+last+" IN model META label='metadata-only'")
				for _, area := range []string{"GET AREA " + first + " TO " + last + " FROM model", "GET AREA AROUND " + last + " RADIUS 2 FROM model"} {
					for _, flags := range [][]string{{"--json"}, {"--json", "--blocks"}} {
						response := readModelJSON[struct {
							SchemaVersion uint64 `json:"schema_version"`
							Entries       []modelReadChunk
						}](t, s.ok(t, append(flags, area+" COLUMNS raw,n WITH SCHEMA")...))
						if response.SchemaVersion != 1 || len(response.Entries) != 2 {
							t.Fatalf("area envelope: %+v", response)
						}
						a, b := response.Entries[0], response.Entries[1]
						if a.CX != -1 || a.CY != 0 || dims == 3 && a.CZ != -1 || a.Version != full.Version || a.CommitTimeMS != full.CommitTimeMS || !reflect.DeepEqual(a.ChunkColumns, metadata) || !a.Projected || b.CX != 0 || b.Present != 0 || b.ChunkColumns["label"] != "metadata-only" {
							t.Fatalf("ordered area forms: %+v", response.Entries)
						}
					}
				}
				path := filepath.Join(t.TempDir(), "chunk.bin")
				s.ok(t, "--out", path, get)
				form, err := os.ReadFile(path)
				if err != nil || len(form) < 36 || string(form[:4]) != "CKCF" || binary.LittleEndian.Uint64(form[8:]) != full.Version || binary.LittleEndian.Uint64(form[24:]) != full.CommitTimeMS {
					t.Fatalf("binary identity: %x, %v", form, err)
				}
				sections := make([]uint16, binary.LittleEndian.Uint16(form[32:]))
				for i := range sections {
					sections[i] = binary.LittleEndian.Uint16(form[36+i*12:])
				}
				if !reflect.DeepEqual(sections, []uint16{1, 2, 3, 4, 5, 6}) {
					t.Fatalf("missing real-server section coverage: %v", sections)
				}
				client, err := connect(globalOptions{URI: s.uri, TLSInsecure: tls}, console{})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				schema, err := client.Describe("model")
				if err != nil {
					t.Fatal(err)
				}
				future := appendModelSection(form, 100, []byte{0, 255, 13, 10})
				for _, decode := range []func(*chunkclient.Schema, []string, []byte) (*chunkclient.Chunk, error){chunkclient.DecodeSectionedChunkForm, chunkclient.DecodeSectionedChunkSummary} {
					decoded, err := decode(schema, nil, future)
					if err != nil || !bytes.Equal(decoded.UnknownSections[100], []byte{0, 255, 13, 10}) || decoded.Version != full.Version || decoded.CommitTimeMS != full.CommitTimeMS || decoded.PresentCount() != 1 {
						t.Fatalf("server form with future section: %+v, %v", decoded, err)
					}
					var output bytes.Buffer
					if err := printChunk(&output, decoded, -1, 0, statementOptions{json: true}); err != nil || !strings.Contains(output.String(), `"unknown_sections":[100]`) {
						t.Fatalf("future-section output: %s, %v", &output, err)
					}
					if printed := readModelJSON[modelReadChunk](t, output.String()); !reflect.DeepEqual(printed.ChunkColumns, metadata) {
						t.Fatalf("future section changed known metadata: %+v", printed)
					}
				}
				committed := readModelJSON[modelReadChunk](t, s.ok(t, "--json", "GET CHUNK "+last+" FROM model"))
				for _, statement := range []string{"BEGIN", "SET BLOCK " + block + " IN model n=4"} {
					if _, err := client.Do(statement); err != nil {
						t.Fatal(err)
					}
				}
				var privateOutput bytes.Buffer
				if err := execute(client, nil, "GET AREA "+first+" TO "+last+" FROM model COLUMNS raw,n WITH SCHEMA", statementOptions{json: true, blocks: true}, &privateOutput, console{}); err != nil {
					t.Fatal(err)
				}
				output := privateOutput.String()
				entries := readModelJSON[struct{ Entries []json.RawMessage }](t, output).Entries
				if len(entries) != 2 {
					t.Fatalf("mixed snapshot area: %q", output)
				}
				privateFields := readModelJSON[map[string]json.RawMessage](t, string(entries[0]))
				private := readModelJSON[modelReadChunk](t, string(entries[0]))
				unchanged := readModelJSON[modelReadChunk](t, string(entries[1]))
				if string(privateFields["version"]) != "null" || string(privateFields["commit_time_ms"]) != "null" || string(privateFields["private"]) != "true" || !private.Projected || !reflect.DeepEqual(private.ChunkColumns, metadata) || len(private.Blocks) != 1 || private.Blocks[0].Values["n"] != float64(4) || unchanged.Version != committed.Version || unchanged.CommitTimeMS != committed.CommitTimeMS {
					t.Fatalf("private and committed forms: %q", output)
				}
				privateReply, err := client.Do(get + " COLUMNS raw,n")
				privateForm := privateReply.Bulk
				if err != nil || privateReply.Kind != chunkclient.KindBulk || len(privateForm) < 36 || binary.LittleEndian.Uint16(privateForm[6:]) != 3 || binary.LittleEndian.Uint64(privateForm[8:]) != 0 || binary.LittleEndian.Uint64(privateForm[24:]) != 0 {
					t.Fatalf("projected private header: %x, %v", privateForm, err)
				}
				if _, err := client.Do("ROLLBACK"); err != nil {
					t.Fatal(err)
				}
				if after := readModelJSON[modelReadChunk](t, s.ok(t, "--json", get)); after.Version != full.Version || after.CommitTimeMS != full.CommitTimeMS {
					t.Fatalf("rollback changed producing identity: %+v", after)
				}
			})
		}
	}
}

// Extend an actual server form without changing its known section bodies.
func appendModelSection(form []byte, kind uint16, body []byte) []byte {
	count := int(binary.LittleEndian.Uint16(form[32:]))
	directoryEnd := 36 + count*12
	result := append([]byte{}, form[:directoryEnd]...)
	binary.LittleEndian.PutUint16(result[32:], uint16(count+1))
	for i := range count {
		offset := result[40+i*12:]
		binary.LittleEndian.PutUint32(offset, binary.LittleEndian.Uint32(offset)+12)
	}
	result = binary.LittleEndian.AppendUint16(result, kind)
	result = binary.LittleEndian.AppendUint16(result, 0)
	result = binary.LittleEndian.AppendUint32(result, uint32(len(form)+12))
	result = binary.LittleEndian.AppendUint32(result, uint32(len(body)))
	result = append(result, form[directoryEnd:]...)
	return append(result, body...)
}

func TestCLIChunkModelRetainedIdentities(t *testing.T) {
	for _, dims := range []int{2, 3} {
		for _, tls := range []bool{false, true} {
			t.Run(fmt.Sprintf("%dD/TLS=%t", dims, tls), func(t *testing.T) {
				s := startServerWith(t, tls, false)
				shape, first, last := "1 x 1", "-2 0", "1 0"
				if dims == 3 {
					shape, first, last = "1 x 1 x 1", "-2 0 -3", "1 0 2"
				}
				s.ok(t, "CREATE TABLE retained (n u8) CHUNK "+shape)
				s.ok(t, "SET BLOCK "+first+" IN retained n=9")
				deleted, err := strconv.ParseUint(strings.TrimSpace(s.ok(t, "DELETE BLOCK "+first+" FROM retained")), 10, 64)
				if err != nil {
					t.Fatal(err)
				}
				s.ok(t, "SET BLOCK "+last+" IN retained n=7")
				get := "GET CHUNK " + first + " FROM retained"
				empty := readModelJSON[modelReadChunk](t, s.ok(t, "--json", "--blocks", get))
				if empty.Version != deleted || empty.CommitTimeMS == 0 || empty.Present != 0 || len(empty.Blocks) != 0 {
					t.Fatalf("written empty form: %+v", empty)
				}
				// This checkpoints the empty chunk but retains its newer deletion.
				s.ok(t, fmt.Sprintf("COMPACT TOMBSTONES retained BEFORE %d", deleted))
				stats := readModelJSON[map[string]any](t, s.ok(t, "--json", "SHOW TABLE STATS retained"))
				if stats["tombstones"] != float64(1) || stats["dims"] != float64(dims) {
					t.Fatalf("expected a collected tombstone: %v", stats)
				}
				retained := readModelJSON[modelReadChunk](t, s.ok(t, "--json", "--blocks", get))
				if !reflect.DeepEqual(retained, empty) {
					t.Fatalf("collection changed producing identity: %+v, %+v", empty, retained)
				}
				area := readModelJSON[[]modelReadChunk](t, s.ok(t, "--json", "GET AREA "+first+" TO "+last+" FROM retained"))
				if len(area) != 1 || area[0].CX != 1 {
					t.Fatalf("ordinary area included deletion: %+v", area)
				}
				type identityEntry struct {
					CX, CY, CZ int64
					Version    uint64
					Deleted    bool
					Chunk      *modelReadChunk
				}
				type identityPage struct {
					Chunks                []identityEntry
					More                  bool
					Epoch                 string
					Position              *uint64
					RetainedDeletionFloor uint64 `json:"retained_deletion_floor"`
				}
				since := fmt.Sprintf(" SINCE %d", deleted-1)
				page := readModelJSON[identityPage](t, s.ok(t, "--json", "--blocks", "SCAN CHUNKS FROM retained LIMIT 1"+since+" WITH DATA"))
				if len(page.Chunks) != 1 || !page.More || len(page.Epoch) != 32 || page.Position == nil || *page.Position < area[0].Version || page.RetainedDeletionFloor != deleted-1 || page.Chunks[0].CX != -2 || dims == 3 && page.Chunks[0].CZ != -3 || page.Chunks[0].Version != deleted || !page.Chunks[0].Deleted || page.Chunks[0].Chunk != nil {
					t.Fatalf("deletion page: %+v", page)
				}
				next := readModelJSON[identityPage](t, s.ok(t, "--json", "--blocks", "SCAN CHUNKS FROM retained AFTER "+first+since+" WITH DATA COLUMNS n"))
				if len(next.Chunks) != 1 || next.More || next.Epoch != page.Epoch || next.Chunks[0].Deleted || next.Chunks[0].CX != 1 || dims == 3 && next.Chunks[0].CZ != 2 || next.Chunks[0].Chunk == nil || next.Chunks[0].Chunk.CommitTimeMS != area[0].CommitTimeMS || next.Chunks[0].Chunk.Version != next.Chunks[0].Version || !next.Chunks[0].Chunk.Projected {
					t.Fatalf("live page: %+v", next)
				}
				bare := readModelJSON[struct{ Chunks [][]uint64 }](t, s.ok(t, "--json", "SCAN CHUNKS FROM retained AFTER "+first))
				coordinates := []uint64{1, 0}
				if dims == 3 {
					coordinates = append(coordinates, 2)
				}
				row := append(coordinates, next.Chunks[0].Version)
				if !reflect.DeepEqual(bare.Chunks, [][]uint64{row}) {
					t.Fatalf("identity-only dimensional row: %+v", bare)
				}
				reader := startServerConfigured(t, tls, false, 4, "--data-dir", s.dataDirectory, "--read-only")
				readOnlyOutput := reader.ok(t, "--json", "SCAN CHUNKS FROM retained"+since+" WITH DATA")
				readonly := readModelJSON[identityPage](t, readOnlyOutput)
				if readonly.Position != nil || readonly.Epoch != page.Epoch || len(readonly.Chunks) != 2 || !readonly.Chunks[0].Deleted || readonly.Chunks[1].Chunk == nil || readonly.Chunks[1].Chunk.CommitTimeMS != area[0].CommitTimeMS {
					t.Fatalf("read-only page: %+v", readonly)
				}
				if fields := readModelJSON[map[string]json.RawMessage](t, readOnlyOutput); string(fields["position"]) != "null" {
					t.Fatalf("read-only position must be present and null: %s", readOnlyOutput)
				}
				s.ok(t, fmt.Sprintf("COMPACT TOMBSTONES retained BEFORE %d", deleted+1))
				if out := s.ok(t, "--json", get); out != "null\n" {
					t.Fatalf("forgotten identity: %q", out)
				}
				code, out, failure := s.cli(t, "", "--json", "SCAN CHUNKS FROM retained"+since+" WITH DATA")
				resync := readModelJSON[struct {
					Error struct {
						Code string
						Keys map[string]any
					}
				}](t, failure)
				current, haveCurrent := resync.Error.Keys["current"].(float64)
				if code != 1 || out != "" || resync.Error.Code != "RESYNC_REQUIRED" || resync.Error.Keys["reason"] != "history_lost" || resync.Error.Keys["floor"] != strconv.FormatUint(deleted, 10) || resync.Error.Keys["epoch"] != page.Epoch || !haveCurrent || current < float64(deleted) {
					t.Fatalf("forgotten-history error: exit %d, stdout %q, stderr %q", code, out, failure)
				}
			})
		}
	}
}
