package chunkclient

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

// Literal CKCF for a two-block three-axis chunk, with independent column ID 1
// in each domain, an explicitly empty block text value and nullable metadata.
const sectionedLiteral = "434b434601000000090000000000000007000000000000000b000000000000000500000001000000600000000100000002000000610000000200000004000000630000000c000000050000006f0000002500000006000000940000001c0000000200070200000001000000000000000200000001000000000000002400000001000000020000000100000025000000000000000101000000010000000800000000000000020000000000000000000000"

func sectionedFixture(t *testing.T) (*Schema, []byte) {
	t.Helper()
	data, err := hex.DecodeString(sectionedLiteral)
	if err != nil {
		t.Fatal(err)
	}
	return &Schema{Version: 7, Dims: 3, ChunkWidth: 2, ChunkHeight: 1, ChunkDepth: 1,
		Columns: []Column{{ID: 1, Name: "n", Type: ColumnType{Kind: ColumnUnsigned, Size: 8}},
			{ID: 2, Name: "text", Type: ColumnType{Kind: ColumnText, Size: 8}, Null: true}},
		ChunkColumns: []Column{{ID: 1, Name: "flag", Type: ColumnType{Kind: ColumnBool, Size: 1}},
			{ID: 2, Name: "label", Type: ColumnType{Kind: ColumnText, Size: 8}, Null: true}},
		columnIndexes: map[string]int{"n": 0, "text": 1}}, data
}

// Test assembly writes explicit section records without using the parser.
func sectionedTestForm(header []byte, kinds []uint16, bodies [][]byte) []byte {
	data := bytes.Clone(header[:36])
	binary.LittleEndian.PutUint16(data[32:], uint16(len(kinds)))
	next := 36 + 12*len(kinds)
	for i, kind := range kinds {
		data = binary.LittleEndian.AppendUint16(data, kind)
		data = binary.LittleEndian.AppendUint16(data, 0)
		data = binary.LittleEndian.AppendUint32(data, uint32(next))
		data = binary.LittleEndian.AppendUint32(data, uint32(len(bodies[i])))
		next += len(bodies[i])
	}
	for _, body := range bodies {
		data = append(data, body...)
	}
	return data
}

func TestSectionedChunkLiteralAndMetadata(t *testing.T) {
	schema, data := sectionedFixture(t)
	chunk, err := DecodeSectionedChunkForm(schema, nil, data)
	if err != nil {
		t.Fatal(err)
	}
	if chunk.Version != 9 || chunk.SchemaVersion != 7 || chunk.CommitTimeMS != 11 || chunk.Depth != 1 || chunk.Present[0] || !chunk.Present[1] || chunk.ValueAt(0, 1).Text != "7" || chunk.ValueAt(1, 1).Kind != KindBulk || len(chunk.ValueAt(1, 1).Bulk) != 0 {
		t.Fatal(chunk)
	}
	if !chunk.MetadataValues[0].Bool || chunk.MetadataValues[1].Kind != KindNull {
		t.Fatal(chunk.MetadataValues)
	}
	if len(chunk.Values[0]) != 1 || len(chunk.Values[1]) != 1 || chunk.ValueAt(0, 0).Kind != KindNull {
		t.Fatal("absent blocks allocated dense value cells")
	}
	if err := ValidateSectionedChunkMutation(schema, nil, data); err != nil {
		t.Fatal(err)
	}
	projected := sectionedTestForm(data, []uint16{1, 2, 5, 6}, [][]byte{data[96:97], data[97:99], data[111:148], data[148:]})
	binary.LittleEndian.PutUint16(projected[6:], chunkFormProjected)
	projection, err := DecodeSectionedChunkForm(schema, []string{"n"}, projected)
	if err != nil || len(projection.MetadataValues) != 2 || len(projection.Values) != 1 {
		t.Fatal(projection, err)
	}
	partial := sectionedTestForm(data, []uint16{1, 2, 6}, [][]byte{data[96:97], data[97:99], data[148:]})
	binary.LittleEndian.PutUint16(partial[6:], chunkFormProjected)
	if err := ValidateSectionedChunkMutation(schema, []string{"n"}, partial); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeSectionedChunkForm(schema, []string{"n"}, partial); err == nil {
		t.Fatal("read accepted missing metadata")
	}
}

func TestSectionedUnknownReadsRefuseMutations(t *testing.T) {
	schema, data := sectionedFixture(t)
	schema.Columns[0].Type = ColumnType{Kind: ColumnUnknown, Name: "future(8)"}
	schema.ChunkColumns[0].Type = ColumnType{Kind: ColumnUnknown, Name: "future(1)"}
	chunk, err := DecodeSectionedChunkForm(schema, nil, data)
	if err != nil || !bytes.Equal(chunk.ValueAt(0, 1).Bulk, []byte{7}) || !bytes.Equal(chunk.MetadataValues[0].Bulk, []byte{1}) {
		t.Fatal(chunk, err)
	}
	if err := ValidateSectionedChunkMutation(schema, nil, data); err == nil {
		t.Fatal("unknown mutation accepted")
	}
	schema, data = sectionedFixture(t)
	future := sectionedTestForm(data, []uint16{1, 2, 4, 5, 6, 9}, [][]byte{data[96:97], data[97:99], data[99:111], data[111:148], data[148:], []byte("opaque")})
	chunk, err = DecodeSectionedChunkForm(schema, nil, future)
	if err != nil || string(chunk.UnknownSections[9]) != "opaque" {
		t.Fatal(chunk, err)
	}
	if err := ValidateSectionedChunkMutation(schema, nil, future); err == nil {
		t.Fatal("unknown section mutation accepted")
	}
}

func TestSectionedReadIgnoresLoweredQuota(t *testing.T) {
	schema, data := sectionedFixture(t)
	schema.Options = []MapEntry{{Key: Value{Kind: KindBulk, Bulk: []byte("var_max_chunk_bytes")}, Value: Value{Kind: KindInteger, Text: "1"}}}
	if _, err := DecodeSectionedChunkForm(schema, nil, data); err != nil {
		t.Fatal(err)
	}
}

func TestSectionedSparseValuesStayBounded(t *testing.T) {
	schema, header := sectionedFixture(t)
	schema.ChunkWidth, schema.ChunkHeight, schema.ChunkDepth = 1024, 1024, 1
	schema.ChunkColumns = nil
	schema.Columns = make([]Column, 1024)
	for i := range schema.Columns {
		schema.Columns[i] = Column{ID: uint32(i + 1), Name: fmt.Sprintf("v%d", i), Type: ColumnType{Kind: ColumnText, Size: 8}, Null: true}
	}
	presence := make([]byte, 1048576/8)
	for _, present := range []bool{false, true} {
		var variable []byte
		if present {
			presence[0] = 1
			presence[len(presence)-1] = 128
			variable = binary.LittleEndian.AppendUint32(variable, 1024)
			variable = binary.LittleEndian.AppendUint32(variable, 0)
			variable = binary.LittleEndian.AppendUint32(variable, 1)
			variable = append(variable, 'a')
			variable = binary.LittleEndian.AppendUint32(variable, 1024)
			variable = binary.LittleEndian.AppendUint32(variable, 1048575)
			variable = binary.LittleEndian.AppendUint32(variable, 1)
			variable = append(variable, 'z')
		}
		data := sectionedTestForm(header, []uint16{1, 4, 6}, [][]byte{presence, variable, {0, 0, 0, 0}})
		chunk, err := DecodeSectionedChunkForm(schema, nil, data)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if present {
			want = 2
		}
		for i := range schema.Columns {
			if len(chunk.Values[i]) != want {
				t.Fatal("unbounded sparse column", i)
			}
			if present && i == 1023 {
				if value := chunk.ValueAt(i, 0); value.Kind != KindBulk || string(value.Bulk) != "a" {
					t.Fatal("lost first-block value", value)
				}
				if value := chunk.ValueAt(i, 1048575); value.Kind != KindBulk || string(value.Bulk) != "z" {
					t.Fatal("lost last-block value", value)
				}
			} else if chunk.ValueAt(i, 0).Kind != KindNull || chunk.ValueAt(i, 1048575).Kind != KindNull {
				t.Fatal("unexpected sparse value", i)
			}
		}
	}
	// A million dense cells are large enough to exercise admission while a
	// missing guard remains bounded during the regression mutation check.
	schema.ChunkHeight = 1
	data := sectionedTestForm(header, []uint16{1, 6}, [][]byte{bytes.Repeat([]byte{255}, 128), {0, 0, 0, 0}})
	if _, err := DecodeSectionedChunkForm(schema, nil, data); err == nil || !strings.Contains(err.Error(), "present column values") {
		t.Fatal(err)
	}
	if err := ValidateSectionedChunkMutation(schema, nil, data); err != nil {
		t.Fatal("raw validation expanded decoded cells", err)
	}
}

func TestSectionedChunkRejectsMalformedData(t *testing.T) {
	schema, data := sectionedFixture(t)
	for name, change := range map[string]func([]byte){
		"magic":                 func(b []byte) { b[0] = 'X' },
		"flags":                 func(b []byte) { b[6] = 8 },
		"reserved":              func(b []byte) { b[34] = 1 },
		"schema":                func(b []byte) { b[16] = 8 },
		"private identity":      func(b []byte) { b[6] = 2 },
		"projection":            func(b []byte) { b[6] = 1 },
		"section overlap":       func(b []byte) { b[52] = 96 },
		"presence padding":      func(b []byte) { b[96] = 128 },
		"absent variable":       func(b []byte) { b[103] = 0 },
		"metadata bool":         func(b []byte) { b[147] = 2 },
		"metadata null payload": func(b []byte) { b[143] = 1 },
		"descriptor width":      func(b []byte) { b[156] = 7 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := bytes.Clone(data)
			change(bad)
			if _, err := DecodeSectionedChunkForm(schema, nil, bad); err == nil {
				t.Fatal("malformed form accepted")
			}
		})
	}
	for _, length := range []int{0, 35, 95, len(data) - 1} {
		if _, err := DecodeSectionedChunkForm(schema, nil, data[:length]); err == nil {
			t.Fatal("truncated form accepted", length)
		}
	}
}
