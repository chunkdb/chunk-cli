package chunkclient

import (
	"math"
	"strings"
	"testing"
)

func TestChunkFormLimitAdmission(t *testing.T) {
	base := Schema{Table: "world", Dims: 2, ChunkWidth: 2, ChunkHeight: 3, Columns: []Column{{Name: "n", Type: ColumnType{Kind: ColumnUnsigned, Size: 3}, Null: true}, {Name: "s", Type: ColumnType{Kind: ColumnText, Size: 8}}, {Name: "b", Type: ColumnType{Kind: ColumnBytes, Size: 8}}}, Options: []MapEntry{{Key: Value{Kind: KindBulk, Bulk: []byte("var_max_chunk_bytes")}, Value: Value{Kind: KindInteger, Text: "40"}}}}
	// The structural bound includes checked sections and the hard variable ceiling.
	if got, err := ChunkFormLimit(&base); err != nil || got != 67109005 {
		t.Fatalf("limit %d: %v", got, err)
	}
	cases := []struct {
		name   string
		schema Schema
		want   string
	}{
		{"unknown", base, "future_payload"}, {"dimension", base, "geometry"}, {"invalid budget", base, "variable byte limit"}, {"multiplication overflow", base, "geometry"}, {"addition overflow", base, "variable byte limit"},
	}
	cases[0].schema.Columns = []Column{{Name: "future_payload", Type: ColumnType{Kind: ColumnUnknown, Name: "vector(7)"}}}
	cases[1].schema.Dims = 3
	cases[2].schema.Options = []MapEntry{{Key: Value{Kind: KindBulk, Bulk: []byte("var_max_chunk_bytes")}, Value: Value{Kind: KindBigNumber, Text: "18446744073709551616"}}}
	cases[3].schema.ChunkWidth = math.MaxInt
	cases[3].schema.ChunkHeight = math.MaxInt
	cases[4].schema.Options = []MapEntry{{Key: Value{Kind: KindBulk, Bulk: []byte("var_max_chunk_bytes")}, Value: Value{Kind: KindBigNumber, Text: "18446744073709551615"}}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ChunkFormLimit(&tc.schema)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("admission: %v", err)
			}
			if tc.name == "unknown" && !strings.Contains(err.Error(), "vector(7)") {
				t.Fatal("lost retained type", err)
			}
		})
	}
}
