package chunkclient

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
)

// Chunk is a chunk form (GET CHUNK, GET AREA) decoded with its table's
// schema.
type Chunk struct {
	// Version is the chunk version the form carries.
	Version uint64
	Width   int
	Height  int
	// Present has one entry per block, row by row.
	Present []bool
	// Columns are the columns the form holds: the COLUMNS of the read in
	// their order, else every column in schema order.
	Columns []Column
	// Values holds per column (as Columns) one value per block; an absent
	// block's value is null.
	Values [][]Value
}

// PresentCount is the number of present blocks.
func (c *Chunk) PresentCount() int {
	count := 0
	for _, present := range c.Present {
		if present {
			count++
		}
	}
	return count
}

// DecodeChunkForm decodes a chunk form: the version (u64 little-endian), the
// presence bitmap, per fixed-width column of the form its values and, for a
// NULL column, its validity bits (each padded to a byte), then entries of
// column id (u32, as DESCRIBE reports it), block index (u32), length (u32)
// and the bytes. columns are
// the COLUMNS of the read, or empty for every column.
func DecodeChunkForm(schema *Schema, columns []string, form []byte) (*Chunk, error) {
	chunk := &Chunk{Width: schema.ChunkWidth, Height: schema.ChunkHeight}
	blocks := schema.BlockCount()
	maskBytes := (blocks + 7) / 8

	if len(form) < 8+maskBytes {
		return nil, fmt.Errorf("the chunk form has %d bytes, a chunk of table %s needs at least %d", len(form), schema.Table, 8+maskBytes)
	}
	if len(columns) == 0 {
		chunk.Columns = schema.Columns
	} else {
		for _, name := range columns {
			column, ok := schema.Column(name)
			if !ok {
				return nil, fmt.Errorf("table %s has no column %s", schema.Table, name)
			}
			chunk.Columns = append(chunk.Columns, column)
		}
	}

	// The fixed part: version, presence, then the sections.
	need := 8 + maskBytes
	for _, column := range chunk.Columns {
		need += sectionBytes(column, blocks)
	}
	if len(form) < need {
		return nil, fmt.Errorf("the chunk form has %d bytes, the schema of table %s (version %d) needs at least %d",
			len(form), schema.Table, schema.Version, need)
	}
	chunk.Version = binary.LittleEndian.Uint64(form)
	presence := form[8 : 8+maskBytes]
	chunk.Present = make([]bool, blocks)
	for i := range blocks {
		chunk.Present[i] = bit(presence, i)
	}

	offset := 8 + maskBytes
	chunk.Values = make([][]Value, len(chunk.Columns))
	var varColumns []int
	for index, column := range chunk.Columns {
		if !column.Type.FixedWidth() {
			varColumns = append(varColumns, index)
			continue
		}
		width := column.Type.Bits()
		valueBytes := (blocks*width + 7) / 8
		values := form[offset : offset+valueBytes]
		offset += valueBytes
		var validity []byte
		if column.Null {
			validity = form[offset : offset+maskBytes]
			offset += maskBytes
		}
		decoded := make([]Value, blocks)
		for i := range blocks {
			if !chunk.Present[i] || (validity != nil && !bit(validity, i)) {
				decoded[i] = Value{Kind: KindNull}
				continue
			}
			decoded[i] = fixedValue(column.Type, values, i*width)
		}
		chunk.Values[index] = decoded
	}

	// Entries name their column by the id DESCRIBE reports.
	columnOfID := map[uint32]int{}
	for _, index := range varColumns {
		column := chunk.Columns[index]
		columnOfID[column.ID] = index
		values := make([]Value, blocks)
		for i := range blocks {
			if !chunk.Present[i] || column.Null {
				values[i] = Value{Kind: KindNull}
			} else {
				values[i] = Value{Kind: KindBulk, Bulk: []byte{}}
			}
		}
		chunk.Values[index] = values
	}

	for offset < len(form) {
		if len(form)-offset < 12 {
			return nil, fmt.Errorf("the chunk form ends inside a text or bytes entry at byte %d", offset)
		}
		id := binary.LittleEndian.Uint32(form[offset:])
		block := int(binary.LittleEndian.Uint32(form[offset+4:]))
		length := int(binary.LittleEndian.Uint32(form[offset+8:]))
		offset += 12
		if length > len(form)-offset {
			return nil, fmt.Errorf("a text or bytes entry of %d bytes runs past the chunk form", length)
		}
		if block >= blocks || !chunk.Present[block] {
			return nil, fmt.Errorf("a text or bytes entry names block %d, which is not a present block", block)
		}
		index, ok := columnOfID[id]
		if !ok {
			return nil, fmt.Errorf("a text or bytes entry names column id %d, which is not a text or bytes column of the form (table %s, schema version %d)",
				id, schema.Table, schema.Version)
		}
		chunk.Values[index][block] = Value{Kind: KindBulk, Bulk: form[offset : offset+length]}
		offset += length
	}
	return chunk, nil
}

// sectionBytes is the size of a column's part of the payload.
func sectionBytes(column Column, blocks int) int {
	if !column.Type.FixedWidth() {
		return 0
	}
	size := (blocks*column.Type.Bits() + 7) / 8
	if column.Null {
		size += (blocks + 7) / 8
	}
	return size
}

func bit(data []byte, index int) bool {
	return data[index/8]>>(index%8)&1 == 1
}

// extractBits copies n bits starting at bit start, lowest bit first.
func extractBits(data []byte, start, n int) []byte {
	out := make([]byte, (n+7)/8)
	for i := range n {
		if bit(data, start+i) {
			out[i/8] |= 1 << (i % 8)
		}
	}
	return out
}

func fixedValue(t ColumnType, data []byte, start int) Value {
	raw := extractBits(data, start, t.Bits())
	if t.Kind == ColumnBits {
		return Value{Kind: KindBulk, Bulk: raw}
	}
	var word uint64
	for i, b := range raw {
		word |= uint64(b) << (8 * i)
	}
	switch t.Kind {
	case ColumnUnsigned:
		return Value{Kind: KindInteger, Text: strconv.FormatUint(word, 10)}
	case ColumnSigned:
		shift := 64 - t.Size
		return Value{Kind: KindInteger, Text: strconv.FormatInt(int64(word<<shift)>>shift, 10)}
	case ColumnBool:
		return Value{Kind: KindBool, Bool: word == 1}
	case ColumnFloat32:
		return Value{Kind: KindDouble, Text: FormatFloat(float64(math.Float32frombits(uint32(word))), 32)}
	default:
		return Value{Kind: KindDouble, Text: FormatFloat(math.Float64frombits(word), 64)}
	}
}

// FormatFloat writes a float as the server does: the shortest text that
// reads back to the same value, fixed or scientific, whichever is shorter;
// inf, -inf and nan.
func FormatFloat(value float64, bitSize int) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	fixed := strconv.FormatFloat(value, 'f', -1, bitSize)
	scientific := strconv.FormatFloat(value, 'e', -1, bitSize)
	if len(scientific) < len(fixed) {
		return scientific
	}
	return fixed
}
