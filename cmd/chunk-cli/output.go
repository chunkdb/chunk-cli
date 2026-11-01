package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/big"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
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
	case chunkclient.KindSimple, chunkclient.KindInteger, chunkclient.KindBigNumber, chunkclient.KindDouble:
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
	fmt.Fprintf(&out, "table = %s\nversion = %d\nchunk = %d x %d", schema.Table, schema.Version, schema.ChunkWidth, schema.ChunkHeight)
	if schema.Dims == 3 {
		fmt.Fprintf(&out, " x %d", schema.ChunkDepth)
	}
	fmt.Fprintf(&out, "\nlarge = %d x %d", schema.LargeWidth, schema.LargeHeight)
	if schema.Dims == 3 {
		fmt.Fprintf(&out, " x %d", schema.LargeDepth)
	}
	out.WriteByte('\n')
	if schema.Epoch != "" {
		fmt.Fprintf(&out, "epoch = %s\nrevision = ", schema.Epoch)
		if schema.Revision == nil {
			out.WriteString("NULL\n")
		} else {
			fmt.Fprintf(&out, "%d\n", *schema.Revision)
		}
		fmt.Fprintf(&out, "dims = %d\n", schema.Dims)
	} else if schema.Dims == 3 {
		out.WriteString("dims = 3\n")
	}
	out.WriteString("columns:\n")
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
	if len(schema.ChunkColumns) > 0 {
		out.WriteString("chunk_columns:\n")
		for _, column := range schema.ChunkColumns {
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
	if reply.Kind != chunkclient.KindArray || len(reply.Items) < len(columns) {
		return fmt.Errorf("GET BLOCK: expected %d values, got a %s of %d (the table may have changed)", len(columns), reply.Kind, len(reply.Items))
	}
	for i, column := range columns {
		if err := validateWatchValue(reply.Items[i], column); err != nil {
			return fmt.Errorf("GET BLOCK: %w", err)
		}
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

func printChunk(w io.Writer, chunk *chunkclient.Chunk, cx, cy int64, opts statementOptions, z ...int64) error {
	if opts.json {
		return writeJSON(w, chunkJSON(chunk, cx, cy, opts.blocks, z...))
	}
	var out bytes.Buffer
	writeChunk(&out, chunk, cx, cy, opts.blocks, "", z...)
	_, err := w.Write(out.Bytes())
	return err
}

func printArea(w io.Writer, schema *chunkclient.Schema, names []string, reply chunkclient.Value, opts statementOptions) error {
	if reply.Kind != chunkclient.KindArray {
		return fmt.Errorf("GET AREA: expected an array, got a %s", reply.Kind)
	}
	type entry struct {
		coordinates []int64
		chunk       *chunkclient.Chunk
	}
	dims := schema.Dims
	if dims == 0 {
		dims = 2
	}
	entries := make([]entry, 0, len(reply.Items))
	for _, item := range reply.Items {
		if item.Kind != chunkclient.KindArray || len(item.Items) < dims+1 || item.Items[dims].Kind != chunkclient.KindBulk {
			return fmt.Errorf("GET AREA: invalid %d-axis chunk entry", dims)
		}
		coordinates := make([]int64, dims)
		for axis := range coordinates {
			var err error
			coordinates[axis], err = item.Items[axis].Int64()
			if err != nil {
				return fmt.Errorf("GET AREA: invalid chunk coordinate")
			}
		}
		chunk, err := decodeOutputChunk(schema, names, item.Items[dims].Bulk, opts.blocks)
		if err != nil {
			return fmt.Errorf("GET AREA: chunk %v: %w", coordinates, err)
		}
		entries = append(entries, entry{coordinates, chunk})
	}
	if opts.json {
		list := make([]any, len(entries))
		for i, e := range entries {
			fields := orderedMap{}
			for axis, value := range e.coordinates {
				fields = append(fields, jsonField{[]string{"cx", "cy", "cz"}[axis], value})
			}
			list[i] = append(fields, chunkJSON(e.chunk, e.coordinates[0], e.coordinates[1], opts.blocks, e.coordinates[2:]...)...)
		}
		return writeJSON(w, list)
	}
	var out bytes.Buffer
	if len(entries) == 0 {
		out.WriteString("(empty)\n")
	}
	for _, e := range entries {
		out.WriteString("chunk")
		for _, coordinate := range e.coordinates {
			fmt.Fprintf(&out, " %d", coordinate)
		}
		out.WriteByte('\n')
		writeChunk(&out, e.chunk, e.coordinates[0], e.coordinates[1], opts.blocks, "  ", e.coordinates[2:]...)
	}
	_, err := w.Write(out.Bytes())
	return err
}

func decodeOutputChunk(schema *chunkclient.Schema, names []string, data []byte, blocks bool) (*chunkclient.Chunk, error) {
	if schema.Dims == 3 {
		if blocks {
			return chunkclient.DecodeSectionedChunkForm(schema, names, data)
		}
		return chunkclient.DecodeSectionedChunkSummary(schema, names, data)
	}
	return chunkclient.DecodeChunkForm(schema, names, data)
}

func blockAxes(chunk *chunkclient.Chunk, cx, cy int64, i int, z []int64) ([]any, []string) {
	coordinates := []int64{cx, cy}
	widths := []int{chunk.Width, chunk.Height}
	local := []int{i % chunk.Width, i / chunk.Width % chunk.Height}
	if len(z) != 0 {
		coordinates = append(coordinates, z[0])
		widths = append(widths, chunk.Depth)
		local = append(local, i/(chunk.Width*chunk.Height))
	}
	values, text := make([]any, len(coordinates)), make([]string, len(coordinates))
	for axis, coordinate := range coordinates {
		absolute := new(big.Int).Mul(big.NewInt(coordinate), big.NewInt(int64(widths[axis])))
		absolute.Add(absolute, big.NewInt(int64(local[axis])))
		if absolute.IsInt64() {
			values[axis], text[axis] = absolute.Int64(), absolute.String()
		} else {
			values[axis], text[axis] = []any{coordinate, local[axis]}, fmt.Sprintf("[%d,%d]", coordinate, local[axis])
		}
	}
	return values, text
}

func chunkBlockCount(chunk *chunkclient.Chunk) int {
	depth := chunk.Depth
	if depth == 0 {
		depth = 1
	}
	return chunk.Width * chunk.Height * depth
}

func unknownChunkSections(chunk *chunkclient.Chunk) []int {
	kinds := make([]int, 0, len(chunk.UnknownSections))
	for kind := range chunk.UnknownSections {
		kinds = append(kinds, int(kind))
	}
	sort.Ints(kinds)
	return kinds
}

func writeChunk(out *bytes.Buffer, chunk *chunkclient.Chunk, cx, cy int64, blocks bool, indent string, z ...int64) {
	version := strconv.FormatUint(chunk.Version, 10)
	if chunk.Private {
		version = "NULL"
	}
	fmt.Fprintf(out, "%sversion = %s\n%sschema_version = %d\n", indent, version, indent, chunk.SchemaVersion)
	if chunk.Depth != 0 {
		timestamp := strconv.FormatUint(chunk.CommitTimeMS, 10)
		if chunk.Private {
			timestamp = "NULL"
		}
		fmt.Fprintf(out, "%scommit_time_ms = %s\n", indent, timestamp)
	}
	fmt.Fprintf(out, "%spresent = %d of %d blocks\n", indent, chunk.PresentCount(), chunkBlockCount(chunk))
	if chunk.Projected {
		fmt.Fprintf(out, "%sprojected = true\n", indent)
	}
	if chunk.Private {
		fmt.Fprintf(out, "%sprivate = true\n", indent)
	}
	if len(chunk.MetadataColumns) != 0 {
		fmt.Fprintf(out, "%schunk_columns:\n", indent)
		for i, column := range chunk.MetadataColumns {
			fmt.Fprintf(out, "%s  %s = %s\n", indent, column.Name, formatValue(chunk.MetadataValues[i], &column.Type))
		}
	}
	if len(chunk.UnknownSections) != 0 {
		fmt.Fprintf(out, "%sunknown_sections = %v\n", indent, unknownChunkSections(chunk))
	}
	if !blocks {
		return
	}
	for i, present := range chunk.Present {
		if !present {
			continue
		}
		_, coordinates := blockAxes(chunk, cx, cy, i, z)
		fmt.Fprintf(out, "%sblock %s\n", indent, strings.Join(coordinates, " "))
		for c, column := range chunk.Columns {
			fmt.Fprintf(out, "%s  %s = %s\n", indent, column.Name, formatValue(chunk.ValueAt(c, i), &column.Type))
		}
	}
}

// JSON output: one document per statement. Integers are exact numbers or decimal strings,
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
		value, err := json.Marshal(exactJSON(field.value))
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
	data, err := json.Marshal(exactJSON(value))
	if err != nil {
		return err
	}
	n, err := fmt.Fprintf(w, "%s\n", data)
	if err == nil && n != len(data)+1 {
		return io.ErrShortWrite
	}
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
	case chunkclient.KindInteger, chunkclient.KindBigNumber:
		return integerJSON(v.Text)
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

// Integer JSON values outside the inclusive JavaScript exact range are decimal strings.
func integerJSON(text string) any {
	number, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return text
	}
	cutoff := big.NewInt(9007199254740991)
	if new(big.Int).Abs(new(big.Int).Set(number)).Cmp(cutoff) > 0 {
		return number.String()
	}
	return json.Number(number.String())
}

func exactJSON(value any) any {
	if value == nil {
		return nil
	}
	if items, ok := value.([]any); ok {
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = exactJSON(item)
		}
		return result
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return integerJSON(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return integerJSON(strconv.FormatUint(v.Uint(), 10))
	}
	return value
}

func chunkJSON(chunk *chunkclient.Chunk, cx, cy int64, blocks bool, z ...int64) orderedMap {
	var version any = chunk.Version
	if chunk.Private {
		version = nil
	}
	fields := orderedMap{
		{"version", version},
		{"schema_version", chunk.SchemaVersion},
		{"present", chunk.PresentCount()},
		{"block_count", chunkBlockCount(chunk)},
	}
	if chunk.Depth != 0 {
		var timestamp any = chunk.CommitTimeMS
		if chunk.Private {
			timestamp = nil
		}
		metadata := orderedMap{}
		for i, column := range chunk.MetadataColumns {
			metadata = append(metadata, jsonField{column.Name, jsonValue(chunk.MetadataValues[i], &column.Type)})
		}
		fields = append(fields, jsonField{"commit_time_ms", timestamp}, jsonField{"chunk_columns", metadata})
	}
	if chunk.Projected {
		fields = append(fields, jsonField{"projected", true})
	}
	if chunk.Private {
		fields = append(fields, jsonField{"private", true})
	}
	if len(chunk.UnknownSections) != 0 {
		fields = append(fields, jsonField{"unknown_sections", unknownChunkSections(chunk)})
	}
	if !blocks {
		return fields
	}
	list := []any{}
	for i, present := range chunk.Present {
		if !present {
			continue
		}
		coordinates, _ := blockAxes(chunk, cx, cy, i, z)
		values := orderedMap{}
		for c, column := range chunk.Columns {
			values = append(values, jsonField{column.Name, jsonValue(chunk.ValueAt(c, i), &column.Type)})
		}
		row := orderedMap{}
		for axis, value := range coordinates {
			row = append(row, jsonField{[]string{"x", "y", "z"}[axis], value})
		}
		list = append(list, append(row, jsonField{"values", values}))
	}
	return append(fields, jsonField{"blocks", list})
}
