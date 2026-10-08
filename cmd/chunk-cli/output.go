package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

// Human output: a value as the CQL literal that writes it (NULL, true,
// 'text', x'bytes', b'bits', numbers), a block as `column = value` lines,
// a map as `key = value` lines, an array as numbered rows.

// formatValue writes a value; typ is the column type when the value is a
// column's, else nil.
func formatValue(v chunkclient.Value, typ *chunkclient.ColumnType) string {
	switch v.Kind {
	case chunkclient.KindNull:
		return "NULL"
	case chunkclient.KindBool:
		return strconv.FormatBool(v.Bool)
	case chunkclient.KindSimple, chunkclient.KindInteger, chunkclient.KindDouble:
		return v.Text
	case chunkclient.KindBulk:
		if typ == nil {
			if printable(v.Bulk, false) {
				return string(v.Bulk)
			}
			return hexLiteral(v.Bulk)
		}
		switch typ.Kind {
		case chunkclient.ColumnText:
			return textLiteral(v.Bulk)
		case chunkclient.ColumnBits:
			return "b'" + bitDigits(v.Bulk, typ.Size) + "'"
		default:
			return hexLiteral(v.Bulk)
		}
	case chunkclient.KindArray:
		parts := make([]string, len(v.Items))
		for i, item := range v.Items {
			parts[i] = formatValue(item, nil)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case chunkclient.KindMap:
		parts := make([]string, len(v.Map))
		for i, entry := range v.Map {
			parts[i] = formatValue(entry.Key, nil) + " = " + formatValue(entry.Value, nil)
		}
		return strings.Join(parts, ", ")
	}
	return "?"
}

// printable reports whether data is UTF-8 without control characters
// (newlines and tabs allowed when multiline).
func printable(data []byte, multiline bool) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, r := range string(data) {
		if r == 0x7f || (r < 0x20 && !(multiline && (r == '\n' || r == '\t'))) {
			return false
		}
	}
	return true
}

// textLiteral quotes text as CQL does ('it”s'); text with control
// characters, which a CQL line cannot hold, is quoted with escapes instead.
func textLiteral(data []byte) string {
	if printable(data, false) {
		return "'" + strings.ReplaceAll(string(data), "'", "''") + "'"
	}
	return strconv.Quote(string(data))
}

func hexLiteral(data []byte) string {
	return "x'" + hex.EncodeToString(data) + "'"
}

// bitDigits writes n bits, the lowest bit first, as b'..' takes them.
func bitDigits(data []byte, n int) string {
	var out strings.Builder
	for i := range n {
		if i/8 < len(data) && data[i/8]>>(i%8)&1 == 1 {
			out.WriteByte('1')
		} else {
			out.WriteByte('0')
		}
	}
	return out.String()
}

func scalar(v chunkclient.Value) bool {
	return v.Kind != chunkclient.KindArray && v.Kind != chunkclient.KindMap
}

// inline reports whether an aggregate fits on one line: its items are
// scalars.
func inline(v chunkclient.Value) bool {
	for _, item := range v.Items {
		if !scalar(item) {
			return false
		}
	}
	for _, entry := range v.Map {
		if !scalar(entry.Value) {
			return false
		}
	}
	return true
}

func printReply(w io.Writer, v chunkclient.Value, opts statementOptions) error {
	if opts.json {
		return writeJSON(w, jsonValue(v, nil))
	}
	var out bytes.Buffer
	writeHuman(&out, v, "")
	_, err := w.Write(out.Bytes())
	return err
}

func writeHuman(out *bytes.Buffer, v chunkclient.Value, indent string) {
	switch v.Kind {
	case chunkclient.KindArray:
		if len(v.Items) == 0 {
			fmt.Fprintf(out, "%s(empty)\n", indent)
		}
		for i, item := range v.Items {
			if scalar(item) || inline(item) {
				fmt.Fprintf(out, "%s%d) %s\n", indent, i+1, formatValue(item, nil))
				continue
			}
			fmt.Fprintf(out, "%s%d)\n", indent, i+1)
			writeHuman(out, item, indent+"  ")
		}
	case chunkclient.KindMap:
		for _, entry := range v.Map {
			key := formatValue(entry.Key, nil)
			if scalar(entry.Value) || (entry.Value.Kind == chunkclient.KindArray && inline(entry.Value)) {
				fmt.Fprintf(out, "%s%s = %s\n", indent, key, formatValue(entry.Value, nil))
				continue
			}
			fmt.Fprintf(out, "%s%s:\n", indent, key)
			writeHuman(out, entry.Value, indent+"  ")
		}
	case chunkclient.KindBulk:
		// Text such as SHOW METRICS prints as it is.
		if printable(v.Bulk, true) {
			out.Write(v.Bulk)
			if !bytes.HasSuffix(v.Bulk, []byte("\n")) {
				out.WriteByte('\n')
			}
			return
		}
		fmt.Fprintf(out, "%s%s\n", indent, hexLiteral(v.Bulk))
	default:
		fmt.Fprintf(out, "%s%s\n", indent, formatValue(v, nil))
	}
}

// printSchema writes DESCRIBE with the columns as CREATE TABLE writes them.
func printSchema(w io.Writer, schema *chunkclient.Schema) error {
	var out bytes.Buffer
	fmt.Fprintf(&out, "table = %s\nversion = %d\nchunk = %d x %d\nlarge = %d x %d\ncolumns:\n",
		schema.Table, schema.Version, schema.ChunkWidth, schema.ChunkHeight, schema.LargeWidth, schema.LargeHeight)
	for _, column := range schema.Columns {
		fmt.Fprintf(&out, "  %s %s", column.Name, column.Type)
		if column.Null {
			out.WriteString(" NULL")
		}
		if column.Required {
			out.WriteString(" REQUIRED")
		}
		if column.Default.Kind != chunkclient.KindNull {
			fmt.Fprintf(&out, " DEFAULT %s", formatValue(column.Default, &column.Type))
		}
		out.WriteByte('\n')
	}
	out.WriteString("options:\n")
	writeHuman(&out, chunkclient.Value{Kind: chunkclient.KindMap, Map: schema.Options}, "  ")
	_, err := w.Write(out.Bytes())
	return err
}

// blockColumns are the columns of a GET BLOCK reply: the COLUMNS, else every
// column in schema order.
func blockColumns(schema *chunkclient.Schema, names []string) ([]chunkclient.Column, error) {
	if len(names) == 0 {
		return schema.Columns, nil
	}
	columns := make([]chunkclient.Column, 0, len(names))
	for _, name := range names {
		column, ok := schema.Column(name)
		if !ok {
			return nil, fmt.Errorf("table %s has no column %s", schema.Table, name)
		}
		columns = append(columns, column)
	}
	return columns, nil
}

func printBlock(w io.Writer, schema *chunkclient.Schema, names []string, reply chunkclient.Value, opts statementOptions) error {
	if reply.Kind == chunkclient.KindNull {
		if opts.json {
			return writeJSON(w, nil)
		}
		_, err := fmt.Fprintln(w, "NULL")
		return err
	}
	columns, err := blockColumns(schema, names)
	if err != nil {
		return err
	}
	if reply.Kind != chunkclient.KindArray || len(reply.Items) != len(columns) {
		return fmt.Errorf("GET BLOCK: expected %d values, got a %s of %d (the table may have changed)", len(columns), reply.Kind, len(reply.Items))
	}
	if opts.json {
		values := make(orderedMap, len(columns))
		for i, column := range columns {
			values[i] = jsonField{column.Name, jsonValue(reply.Items[i], &column.Type)}
		}
		return writeJSON(w, values)
	}
	var out bytes.Buffer
	for i, column := range columns {
		fmt.Fprintf(&out, "%s = %s\n", column.Name, formatValue(reply.Items[i], &column.Type))
	}
	_, err = w.Write(out.Bytes())
	return err
}

func printChunk(w io.Writer, chunk *chunkclient.Chunk, cx, cy int64, opts statementOptions) error {
	if opts.json {
		return writeJSON(w, chunkJSON(chunk, cx, cy, opts.blocks))
	}
	var out bytes.Buffer
	writeChunk(&out, chunk, cx, cy, opts.blocks, "")
	_, err := w.Write(out.Bytes())
	return err
}

func printArea(w io.Writer, schema *chunkclient.Schema, names []string, reply chunkclient.Value, opts statementOptions) error {
	if reply.Kind != chunkclient.KindArray {
		return fmt.Errorf("GET AREA: expected an array, got a %s", reply.Kind)
	}
	type entry struct {
		cx, cy int64
		chunk  *chunkclient.Chunk
	}
	entries := make([]entry, 0, len(reply.Items))
	for _, item := range reply.Items {
		if item.Kind != chunkclient.KindArray || len(item.Items) != 3 || item.Items[2].Kind != chunkclient.KindBulk {
			return fmt.Errorf("GET AREA: expected [cx, cy, chunk form] entries")
		}
		cx, errX := item.Items[0].Int64()
		cy, errY := item.Items[1].Int64()
		if errX != nil || errY != nil {
			return fmt.Errorf("GET AREA: invalid chunk coordinates")
		}
		chunk, err := chunkclient.DecodeChunkForm(schema, names, item.Items[2].Bulk)
		if err != nil {
			return fmt.Errorf("GET AREA: chunk %d %d: %w", cx, cy, err)
		}
		entries = append(entries, entry{cx, cy, chunk})
	}
	if opts.json {
		list := make([]any, len(entries))
		for i, e := range entries {
			list[i] = append(orderedMap{{"cx", e.cx}, {"cy", e.cy}}, chunkJSON(e.chunk, e.cx, e.cy, opts.blocks)...)
		}
		return writeJSON(w, list)
	}
	var out bytes.Buffer
	if len(entries) == 0 {
		out.WriteString("(empty)\n")
	}
	for _, e := range entries {
		fmt.Fprintf(&out, "chunk %d %d\n", e.cx, e.cy)
		writeChunk(&out, e.chunk, e.cx, e.cy, opts.blocks, "  ")
	}
	_, err := w.Write(out.Bytes())
	return err
}

// blockXY is the world coordinates of block index i of chunk cx, cy.
func blockXY(chunk *chunkclient.Chunk, cx, cy int64, i int) (int64, int64) {
	return cx*int64(chunk.Width) + int64(i%chunk.Width), cy*int64(chunk.Height) + int64(i/chunk.Width)
}

func writeChunk(out *bytes.Buffer, chunk *chunkclient.Chunk, cx, cy int64, blocks bool, indent string) {
	fmt.Fprintf(out, "%sversion = %d\n%spresent = %d of %d blocks\n", indent, chunk.Version, indent, chunk.PresentCount(), len(chunk.Present))
	if !blocks {
		return
	}
	for i, present := range chunk.Present {
		if !present {
			continue
		}
		x, y := blockXY(chunk, cx, cy, i)
		fmt.Fprintf(out, "%sblock %d %d\n", indent, x, y)
		for c, column := range chunk.Columns {
			fmt.Fprintf(out, "%s  %s = %s\n", indent, column.Name, formatValue(chunk.Values[c][i], &column.Type))
		}
	}
}

// JSON output: one document per statement. Integers are exact numbers,
// doubles numbers or "inf", "-inf", "nan"; text is a string, bytes a hex
// string, bits a string of 0 and 1 (the lowest bit first).

type jsonField struct {
	key   string
	value any
}

// orderedMap is a JSON object that keeps its keys in order.
type orderedMap []jsonField

func (m orderedMap) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, field := range m {
		if i > 0 {
			out.WriteByte(',')
		}
		key, err := json.Marshal(field.key)
		if err != nil {
			return nil, err
		}
		value, err := json.Marshal(field.value)
		if err != nil {
			return nil, err
		}
		out.Write(key)
		out.WriteByte(':')
		out.Write(value)
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

func writeJSON(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "%s\n", data)
	return err
}

func jsonValue(v chunkclient.Value, typ *chunkclient.ColumnType) any {
	switch v.Kind {
	case chunkclient.KindNull:
		return nil
	case chunkclient.KindBool:
		return v.Bool
	case chunkclient.KindSimple:
		return v.Text
	case chunkclient.KindInteger:
		return json.Number(v.Text)
	case chunkclient.KindDouble:
		if value, err := v.Float64(); err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return v.Text
		}
		return json.Number(v.Text)
	case chunkclient.KindBulk:
		switch {
		case typ == nil && utf8.Valid(v.Bulk):
			return string(v.Bulk)
		case typ == nil:
			return hex.EncodeToString(v.Bulk)
		case typ.Kind == chunkclient.ColumnText:
			return string(v.Bulk)
		case typ.Kind == chunkclient.ColumnBits:
			return bitDigits(v.Bulk, typ.Size)
		default:
			return hex.EncodeToString(v.Bulk)
		}
	case chunkclient.KindArray:
		items := make([]any, len(v.Items))
		for i, item := range v.Items {
			items[i] = jsonValue(item, nil)
		}
		return items
	case chunkclient.KindMap:
		fields := make(orderedMap, len(v.Map))
		for i, entry := range v.Map {
			fields[i] = jsonField{formatValue(entry.Key, nil), jsonValue(entry.Value, nil)}
		}
		return fields
	}
	return nil
}

func chunkJSON(chunk *chunkclient.Chunk, cx, cy int64, blocks bool) orderedMap {
	fields := orderedMap{
		{"version", chunk.Version},
		{"present", chunk.PresentCount()},
		{"block_count", len(chunk.Present)},
	}
	if !blocks {
		return fields
	}
	list := []any{}
	for i, present := range chunk.Present {
		if !present {
			continue
		}
		x, y := blockXY(chunk, cx, cy, i)
		values := orderedMap{}
		for c, column := range chunk.Columns {
			values = append(values, jsonField{column.Name, jsonValue(chunk.Values[c][i], &column.Type)})
		}
		list = append(list, orderedMap{{"x", x}, {"y", y}, {"values", values}})
	}
	return append(fields, jsonField{"blocks", list})
}
