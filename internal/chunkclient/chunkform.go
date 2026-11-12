package chunkclient

import (
	"math"
	"strconv"
)

// Chunk is a chunk form (GET CHUNK, GET AREA) decoded with its table's
// schema.
type Chunk struct {
	// Version is the chunk version the form carries.
	Version uint64
	// SchemaVersion is the schema version the form's columns follow.
	SchemaVersion   uint64
	Width           int
	Height          int
	Depth           int
	CommitTimeMS    uint64
	Projected       bool
	Private         bool
	MetadataColumns []Column
	MetadataValues  []Value
	UnknownSections map[uint16][]byte
	// Present has one entry per block, row by row.
	Present []bool
	// Columns are the columns the form holds: the COLUMNS of the read in
	// their order, else every column in schema order.
	Columns []Column
	// Values stores each column's retained cells; use ValueAt to address blocks.
	Values          [][]Value
	valueIndexes    []uint32
	populatedBlocks int
}

// ValueAt returns one block-column value, including NULL for absent blocks.
func (c *Chunk) ValueAt(column, block int) Value {
	if !c.Present[block] {
		return Value{Kind: KindNull}
	}
	if c.valueIndexes != nil {
		return c.Values[column][int(c.valueIndexes[block])-1]
	}
	return c.Values[column][block]
}

// PresentCount is the number of present blocks.
func (c *Chunk) PresentCount() int {
	if c.Present == nil {
		return c.populatedBlocks
	}
	count := 0
	for _, present := range c.Present {
		if present {
			count++
		}
	}
	return count
}

// DecodeChunkForm reads the checked form with complete chunk metadata.
// columns is its block-column projection, or empty for every block column.
func DecodeChunkForm(schema *Schema, columns []string, form []byte) (*Chunk, error) {
	return DecodeSectionedChunkForm(schema, columns, form)
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
