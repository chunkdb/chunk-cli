package chunkclient

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
)

func TestParseColumnType(t *testing.T) {
	valid := map[string]ColumnType{
		"u1":             {ColumnUnsigned, 1},
		"u64":            {ColumnUnsigned, 64},
		"i2":             {ColumnSigned, 2},
		"i64":            {ColumnSigned, 64},
		"bool":           {ColumnBool, 1},
		"f32":            {ColumnFloat32, 32},
		"f64":            {ColumnFloat64, 64},
		"bits(1)":        {ColumnBits, 1},
		"bits(65535)":    {ColumnBits, 65535},
		"text(256)":      {ColumnText, 256},
		"bytes(16)":      {ColumnBytes, 16},
		"text(16777216)": {ColumnText, 16 << 20},
	}
	for text, want := range valid {
		got, err := ParseColumnType(text)
		if err != nil || got != want {
			t.Errorf("%q: got %+v, %v", text, got, err)
			continue
		}
		if got.String() != text {
			t.Errorf("%q: String() = %q", text, got.String())
		}
	}
	for _, text := range []string{"", "u0", "u65", "i1", "i65", "u", "u08", "u-1", "bits(0)", "bits(65536)", "bits(8",
		"text()", "text(0)", "bytes(16777217)", "f16", "BOOL", "uint8", "text(1x)"} {
		if got, err := ParseColumnType(text); err == nil {
			t.Errorf("%q: expected an error, got %+v", text, got)
		}
	}
}

// describeW is the DESCRIBE reply of the server for
// CREATE TABLE w (id u10 REQUIRED, light u4 DEFAULT 15, f f32 NULL, name text(16) NULL, blob bytes(8)) CHUNK 2 x 2.
const describeW = "%6\r\n$5\r\ntable\r\n$1\r\nw\r\n$7\r\nversion\r\n:1\r\n$7\r\ncolumns\r\n*5\r\n" +
	"%6\r\n$2\r\nid\r\n:1\r\n$4\r\nname\r\n$2\r\nid\r\n$4\r\ntype\r\n$3\r\nu10\r\n$4\r\nnull\r\n#f\r\n$8\r\nrequired\r\n#t\r\n$7\r\ndefault\r\n_\r\n" +
	"%6\r\n$2\r\nid\r\n:2\r\n$4\r\nname\r\n$5\r\nlight\r\n$4\r\ntype\r\n$2\r\nu4\r\n$4\r\nnull\r\n#f\r\n$8\r\nrequired\r\n#f\r\n$7\r\ndefault\r\n:15\r\n" +
	"%6\r\n$2\r\nid\r\n:3\r\n$4\r\nname\r\n$1\r\nf\r\n$4\r\ntype\r\n$3\r\nf32\r\n$4\r\nnull\r\n#t\r\n$8\r\nrequired\r\n#f\r\n$7\r\ndefault\r\n_\r\n" +
	"%6\r\n$2\r\nid\r\n:4\r\n$4\r\nname\r\n$4\r\nname\r\n$4\r\ntype\r\n$8\r\ntext(16)\r\n$4\r\nnull\r\n#t\r\n$8\r\nrequired\r\n#f\r\n$7\r\ndefault\r\n_\r\n" +
	"%6\r\n$2\r\nid\r\n:5\r\n$4\r\nname\r\n$4\r\nblob\r\n$4\r\ntype\r\n$8\r\nbytes(8)\r\n$4\r\nnull\r\n#f\r\n$8\r\nrequired\r\n#f\r\n$7\r\ndefault\r\n_\r\n" +
	"$5\r\nchunk\r\n*2\r\n:2\r\n:2\r\n$5\r\nlarge\r\n*2\r\n:8\r\n:8\r\n$7\r\noptions\r\n%6\r\n" +
	"$15\r\ndurability_mode\r\n$7\r\nrelaxed\r\n$18\r\ncheckpoint_updates\r\n:256\r\n$20\r\ncheckpoint_wal_bytes\r\n:1048576\r\n" +
	"$24\r\nwal_group_commit_updates\r\n:8\r\n$22\r\ncheckpoint_compression\r\n$4\r\nnone\r\n$19\r\nvar_max_chunk_bytes\r\n:1048576\r\n"

func mustSchema(t *testing.T, reply string) *Schema {
	t.Helper()
	value, err := readReply(bufio.NewReader(strings.NewReader(reply)))
	if err != nil {
		t.Fatalf("read DESCRIBE: %v", err)
	}
	schema, err := ParseDescribe(value)
	if err != nil {
		t.Fatalf("parse DESCRIBE: %v", err)
	}
	return schema
}

func TestParseDescribe(t *testing.T) {
	schema := mustSchema(t, describeW)
	if schema.Table != "w" || schema.Version != 1 || schema.ChunkWidth != 2 || schema.ChunkHeight != 2 ||
		schema.LargeWidth != 8 || schema.LargeHeight != 8 || len(schema.Options) != 6 || len(schema.Columns) != 5 {
		t.Fatalf("unexpected schema %+v", schema)
	}
	id, _ := schema.Column("id")
	light, _ := schema.Column("light")
	name, ok := schema.Column("name")
	if !ok || id.ID != 1 || name.ID != 4 || !id.Required || id.Type != (ColumnType{ColumnUnsigned, 10}) || light.Default.Text != "15" ||
		!name.Null || name.Type.Kind != ColumnText || name.Default.Kind != KindNull {
		t.Fatalf("unexpected columns %+v", schema.Columns)
	}
	if _, ok := schema.Column("missing"); ok {
		t.Fatal("found a missing column")
	}

	for _, broken := range []string{
		strings.Replace(describeW, "$3\r\nu10", "$3\r\nu99", 1),
		strings.Replace(describeW, "$5\r\nchunk\r\n*2\r\n:2\r\n:2", "$5\r\nchunk\r\n*2\r\n:2\r\n:0", 1),
		strings.Replace(describeW, "$7\r\nversion\r\n:1", "$7\r\nversioX\r\n:1", 1),
		strings.Replace(describeW, "$4\r\nname\r\n$4\r\nblob", "$4\r\nname\r\n$4\r\nname", 1),
		strings.Replace(describeW, ":5\r\n$4\r\nname", ":4\r\n$4\r\nname", 1),
		strings.Replace(describeW, ":5\r\n$4\r\nname", ":0\r\n$4\r\nname", 1),
		strings.Replace(describeW, "$2\r\nid\r\n:5\r\n", "$2\r\nix\r\n:5\r\n", 1),
	} {
		value, err := readReply(bufio.NewReader(strings.NewReader(broken)))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if _, err := ParseDescribe(value); err == nil {
			t.Errorf("expected an error for %q", broken[:60])
		}
	}
}

// formW is GET CHUNK 0 0 FROM w after
// SET BLOCK 1 0 IN w id = 5, name = 'hi', blob = x'00ff', f = 1.5, as the
// server sent it.
const formW = "\x02\x00\x00\x00\x00\x00\x00\x00" +
	"\x01\x00\x00\x00\x00\x00\x00\x00\x02\x00\x14\x00\x00\x00\xf0\x00\x00\x00\x00\x00\x00\x00\xc0?\x00\x00\x00\x00\x00\x00\x00\x00" +
	"\x02\x04\x00\x00\x00\x01\x00\x00\x00\x02\x00\x00\x00hi\x05\x00\x00\x00\x01\x00\x00\x00\x02\x00\x00\x00\x00\xff"

// formWColumns is GET CHUNK 0 0 FROM w COLUMNS name, light.
const formWColumns = "\x02\x00\x00\x00\x00\x00\x00\x00" +
	"\x01\x00\x00\x00\x00\x00\x00\x00\x02\xf0\x00\x04\x00\x00\x00\x01\x00\x00\x00\x02\x00\x00\x00hi"

func values(chunk *Chunk, column int) string {
	out := make([]string, len(chunk.Values[column]))
	for i, v := range chunk.Values[column] {
		switch v.Kind {
		case KindNull:
			out[i] = "NULL"
		case KindBulk:
			out[i] = "'" + string(v.Bulk) + "'"
		case KindBool:
			if v.Bool {
				out[i] = "true"
			} else {
				out[i] = "false"
			}
		default:
			out[i] = v.Text
		}
	}
	return strings.Join(out, " ")
}

func TestDecodeChunkFormFromServer(t *testing.T) {
	schema := mustSchema(t, describeW)
	chunk, err := DecodeChunkForm(schema, nil, []byte(formW))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if chunk.Version != 2 || chunk.SchemaVersion != 1 || chunk.PresentCount() != 1 || !chunk.Present[1] || len(chunk.Columns) != 5 {
		t.Fatalf("unexpected chunk %+v", chunk)
	}
	for column, want := range []string{"NULL 5 NULL NULL", "NULL 15 NULL NULL", "NULL 1.5 NULL NULL", "NULL 'hi' NULL NULL", "NULL '\x00\xff' NULL NULL"} {
		if got := values(chunk, column); got != want {
			t.Errorf("column %s: got %q, want %q", chunk.Columns[column].Name, got, want)
		}
	}

	subset, err := DecodeChunkForm(schema, []string{"name", "light"}, []byte(formWColumns))
	if err != nil {
		t.Fatalf("decode COLUMNS: %v", err)
	}
	if subset.Columns[0].Name != "name" || values(subset, 0) != "NULL 'hi' NULL NULL" || values(subset, 1) != "NULL 15 NULL NULL" {
		t.Fatalf("unexpected subset %+v", subset)
	}
	if _, err := DecodeChunkForm(schema, []string{"nope"}, []byte(formWColumns)); err == nil {
		t.Fatal("expected an error for an unknown column")
	}
}

// chunkFormBuilder writes chunk forms for a 2 x 2 chunk.
type chunkFormBuilder struct {
	bytes.Buffer
}

// header writes the chunk version and the schema version.
func (b *chunkFormBuilder) header(version, schemaVersion uint64) {
	_ = binary.Write(&b.Buffer, binary.LittleEndian, version)
	_ = binary.Write(&b.Buffer, binary.LittleEndian, schemaVersion)
}

func (b *chunkFormBuilder) u32(v uint32) {
	_ = binary.Write(&b.Buffer, binary.LittleEndian, v)
}

func (b *chunkFormBuilder) entry(id, block uint32, value string) {
	b.u32(id)
	b.u32(block)
	b.u32(uint32(len(value)))
	b.WriteString(value)
}

func testSchema(t *testing.T, version uint64, columns ...Column) *Schema {
	t.Helper()
	schema := &Schema{Table: "t", Version: version, ChunkWidth: 2, ChunkHeight: 2, columnIndexes: map[string]int{}}
	for i, column := range columns {
		if column.ID == 0 {
			column.ID = uint32(i + 1)
		}
		schema.columnIndexes[column.Name] = i
		schema.Columns = append(schema.Columns, column)
	}
	return schema
}

func col(t *testing.T, name, typ string, null bool) Column {
	t.Helper()
	parsed, err := ParseColumnType(typ)
	if err != nil {
		t.Fatal(err)
	}
	return Column{Name: name, Type: parsed, Null: null, Default: Value{Kind: KindNull}}
}

func TestDecodeChunkFormTypes(t *testing.T) {
	schema := testSchema(t, 1,
		col(t, "s", "i4", false), col(t, "ok", "bool", true), col(t, "d", "f64", false),
		col(t, "flags", "bits(3)", false), col(t, "label", "text(8)", false), col(t, "raw", "bytes(4)", true))
	var b chunkFormBuilder
	b.header(7, 1)
	b.WriteByte(0b1101)         // blocks 0, 2, 3 present
	b.Write([]byte{0x8f, 0x07}) // s: -1, -8, 7, 0
	b.WriteByte(0b0101)         // ok values: true, false, true, false
	b.WriteByte(0b1001)         // ok validity: blocks 0 and 3
	for _, bits := range []uint64{0x3ff8000000000000, 0, 0x7ff0000000000000, 0xc000000000000000} {
		_ = binary.Write(&b.Buffer, binary.LittleEndian, bits) // d: 1.5, 0, inf, -2
	}
	b.Write([]byte{0b10_000_101, 0b0000_111_1}) // flags: 5, 0, 6, 7
	b.entry(5, 0, "ab")
	b.entry(5, 3, "z")
	b.entry(6, 2, "\r\n\x00")

	chunk, err := DecodeChunkForm(schema, nil, b.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if chunk.Version != 7 || chunk.PresentCount() != 3 {
		t.Fatalf("unexpected chunk %+v", chunk)
	}
	for column, want := range []string{
		"-1 NULL 7 0",
		"true NULL NULL false",
		"1.5 NULL inf -2",
		"'\x05' NULL '\x06' '\x07'",
		"'ab' NULL '' 'z'",
		"NULL NULL '\r\n\x00' NULL",
	} {
		if got := values(chunk, column); got != want {
			t.Errorf("column %s: got %q, want %q", chunk.Columns[column].Name, got, want)
		}
	}

	// Too short, an entry past the end, an entry of an absent block, an
	// unknown column id.
	full := b.Bytes()
	for name, form := range map[string][]byte{
		"short":         full[:20],
		"cut entry":     full[:len(full)-1],
		"partial entry": append(append([]byte{}, full...), 1, 2, 3),
		"absent block": func() []byte {
			var e chunkFormBuilder
			e.Write(full[:55]) // versions, presence and payload
			e.entry(5, 1, "x")
			return e.Bytes()
		}(),
		"unknown id": func() []byte {
			var e chunkFormBuilder
			e.Write(full)
			e.entry(1, 0, "x")
			return e.Bytes()
		}(),
	} {
		if _, err := DecodeChunkForm(schema, nil, form); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDecodeChunkFormAfterColumnsChanged(t *testing.T) {
	// CREATE TABLE t (a u4, b text(4) NULL) then DROP COLUMN b, ADD COLUMN c
	// text(4) NULL, ADD COLUMN d bytes(4): the ids are 1, 3 and 4.
	withID := func(column Column, id uint32) Column {
		column.ID = id
		return column
	}
	schema := testSchema(t, 4, withID(col(t, "a", "u4", false), 1), withID(col(t, "c", "text(4)", true), 3),
		withID(col(t, "d", "bytes(4)", false), 4))
	var b chunkFormBuilder
	b.header(9, 4)
	b.Write([]byte{0b0011, 0x21, 0x00})
	b.entry(3, 0, "x")
	b.entry(4, 0, "zz")
	b.entry(4, 1, "y")
	chunk, err := DecodeChunkForm(schema, nil, b.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	for column, want := range []string{"1 2 NULL NULL", "'x' NULL NULL NULL", "'zz' 'y' NULL NULL"} {
		if got := values(chunk, column); got != want {
			t.Errorf("column %s: got %q, want %q", chunk.Columns[column].Name, got, want)
		}
	}

	// COLUMNS d: only its entries follow the presence.
	var d chunkFormBuilder
	d.header(9, 4)
	d.WriteByte(0b0011)
	d.entry(4, 1, "y")
	chunk, err = DecodeChunkForm(schema, []string{"d"}, d.Bytes())
	if err != nil || values(chunk, 0) != "'' 'y' NULL NULL" {
		t.Fatalf("got %+v, %v", chunk, err)
	}
	// A form of another schema version is refused.
	older := testSchema(t, 3, schema.Columns...)
	if _, err := DecodeChunkForm(older, nil, b.Bytes()); err == nil || !strings.Contains(err.Error(), "schema version 4") {
		t.Fatalf("schema version mismatch: got %v", err)
	}
	if _, err := DecodeChunkForm(schema, []string{"d"}, []byte{1, 2, 3}); err == nil {
		t.Fatal("expected an error for a form without version and presence")
	}

	// The dropped column's id, a column the form does not hold, and an
	// entry in a table without text or bytes columns are refused.
	for name, form := range map[string]struct {
		columns []string
		id      uint32
		schema  *Schema
	}{
		"dropped id":     {nil, 2, schema},
		"not in COLUMNS": {[]string{"d"}, 3, schema},
		"fixed only":     {nil, 1, testSchema(t, 2, col(t, "a", "u4", false))},
	} {
		var e chunkFormBuilder
		e.header(9, form.schema.Version)
		e.WriteByte(0b0011)
		if form.columns == nil {
			e.Write([]byte{0x21, 0x00})
		}
		e.entry(form.id, 0, "x")
		if _, err := DecodeChunkForm(form.schema, form.columns, e.Bytes()); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestFormatFloat(t *testing.T) {
	for _, tc := range []struct {
		value float64
		bits  int
		want  string
	}{
		{1.5, 64, "1.5"}, {0.1, 32, "0.1"}, {1e21, 64, "1e+21"}, {1234567, 64, "1234567"}, {-0.0001, 64, "-1e-04"}, {100, 64, "100"},
		{1e-7, 64, "1e-07"},
	} {
		if got := FormatFloat(tc.value, tc.bits); got != tc.want {
			t.Errorf("FormatFloat(%v, %d) = %q, want %q", tc.value, tc.bits, got, tc.want)
		}
	}
}
