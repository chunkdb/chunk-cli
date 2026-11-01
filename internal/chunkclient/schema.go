package chunkclient

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// ColumnKind is the family of a column type.
type ColumnKind int

const (
	ColumnUnsigned ColumnKind = iota + 1
	ColumnSigned
	ColumnBool
	ColumnFloat32
	ColumnFloat64
	ColumnBits
	ColumnText
	ColumnBytes
	ColumnUnknown
)

// ColumnType is a column type of DESCRIBE: `uN`, `iN`, `bool`, `f32`, `f64`,
// `bits(N)`, `text(max)` or `bytes(max)`. Size is N for `uN`, `iN` and
// `bits(N)`, and the most bytes for `text` and `bytes`.
type ColumnType struct {
	Kind ColumnKind
	Size int
	Name string
}

const maxVarBytes = 16 << 20

// ParseColumnType parses a type as DESCRIBE writes it.
func ParseColumnType(text string) (ColumnType, error) {
	invalid := fmt.Errorf("unknown column type %q", text)
	switch text {
	case "bool":
		return ColumnType{Kind: ColumnBool, Size: 1}, nil
	case "f32":
		return ColumnType{Kind: ColumnFloat32, Size: 32}, nil
	case "f64":
		return ColumnType{Kind: ColumnFloat64, Size: 64}, nil
	}
	sized := func(kind ColumnKind, digits string, low, high int) (ColumnType, error) {
		if digits == "" || digits[0] == '0' || strings.TrimLeft(digits, "0123456789") != "" {
			return ColumnType{}, invalid
		}
		size, err := strconv.Atoi(digits)
		if err != nil || size < low || size > high {
			return ColumnType{}, invalid
		}
		return ColumnType{Kind: kind, Size: size}, nil
	}
	for _, form := range []struct {
		prefix    string
		kind      ColumnKind
		low, high int
	}{
		{"bits(", ColumnBits, 1, 65535},
		{"text(", ColumnText, 1, maxVarBytes},
		{"bytes(", ColumnBytes, 1, maxVarBytes},
	} {
		if inner, ok := strings.CutPrefix(text, form.prefix); ok {
			digits, closed := strings.CutSuffix(inner, ")")
			if !closed {
				return ColumnType{}, invalid
			}
			return sized(form.kind, digits, form.low, form.high)
		}
	}
	if digits, ok := strings.CutPrefix(text, "u"); ok {
		return sized(ColumnUnsigned, digits, 1, 64)
	}
	if digits, ok := strings.CutPrefix(text, "i"); ok {
		return sized(ColumnSigned, digits, 2, 64)
	}
	return ColumnType{}, invalid
}

func (t ColumnType) String() string {
	if t.Kind == ColumnUnknown {
		return t.Name
	}
	switch t.Kind {
	case ColumnUnsigned:
		return "u" + strconv.Itoa(t.Size)
	case ColumnSigned:
		return "i" + strconv.Itoa(t.Size)
	case ColumnBool:
		return "bool"
	case ColumnFloat32:
		return "f32"
	case ColumnFloat64:
		return "f64"
	case ColumnBits:
		return "bits(" + strconv.Itoa(t.Size) + ")"
	case ColumnText:
		return "text(" + strconv.Itoa(t.Size) + ")"
	case ColumnBytes:
		return "bytes(" + strconv.Itoa(t.Size) + ")"
	}
	return "unknown"
}

// FixedWidth reports whether the column's values are in the chunk payload
// (every type but `text` and `bytes`).
func (t ColumnType) FixedWidth() bool {
	return t.Kind != ColumnText && t.Kind != ColumnBytes
}

// Bits is the width of one value in the chunk payload.
func (t ColumnType) Bits() int {
	if !t.FixedWidth() {
		return 0
	}
	return t.Size
}

// Column is one column of DESCRIBE. Default is a null value when the column
// has no DEFAULT.
type Column struct {
	// ID identifies the column in chunk forms; it stays the same across
	// renames and is never reused.
	ID       uint32
	Name     string
	Type     ColumnType
	Null     bool
	Required bool
	Default  Value
}

// Schema is the DESCRIBE reply of a table.
type Schema struct {
	Epoch         string
	Revision      *uint64
	Dims          int
	ChunkColumns  []Column
	Table         string
	Version       uint64
	Columns       []Column
	ChunkWidth    int
	ChunkHeight   int
	ChunkDepth    int
	LargeWidth    int
	LargeHeight   int
	LargeDepth    int
	Options       []MapEntry
	columnIndexes map[string]int
}

// Column returns the column called name.
func (s *Schema) Column(name string) (Column, bool) {
	index, ok := s.columnIndexes[name]
	if !ok {
		return Column{}, false
	}
	return s.Columns[index], true
}

// BlockCount is the number of blocks of a chunk.
func (s *Schema) BlockCount() int {
	if s == nil || (s.Dims != 0 && s.Dims != 2 && s.Dims != 3) {
		return 0
	}
	depth := 1
	if s.Dims == 3 {
		depth = s.ChunkDepth
	}
	count := 1
	for _, axis := range []int{s.ChunkWidth, s.ChunkHeight, depth} {
		if axis < 1 || axis > 4096 || count > 1048576/axis {
			return 0
		}
		count *= axis
	}
	return count
}

// Describe sends `DESCRIBE <table>` and parses the reply.
func (c *Client) Describe(table string) (*Schema, error) {
	reply, err := c.Do("DESCRIBE " + table)
	if err != nil {
		return nil, err
	}
	return ParseDescribe(reply)
}

// ParseDescribe parses a DESCRIBE reply.
func ParseDescribe(reply Value) (*Schema, error) {
	if reply.Kind != KindMap {
		return nil, fmt.Errorf("DESCRIBE: expected a map, got %s", reply.Kind)
	}
	field := func(key string, kind Kind) (Value, error) {
		value, ok := reply.Lookup(key)
		if !ok {
			return Value{}, fmt.Errorf("DESCRIBE: no %s", key)
		}
		if value.Kind != kind && !(kind == KindInteger && value.Kind == KindBigNumber) {
			return Value{}, fmt.Errorf("DESCRIBE: %s is a %s, expected a %s", key, value.Kind, kind)
		}
		return value, nil
	}
	schema := &Schema{columnIndexes: map[string]int{}}
	table, err := field("table", KindBulk)
	if err != nil {
		return nil, err
	}
	schema.Table = string(table.Bulk)
	schema.Dims = 2
	if dims, found := reply.Lookup("dims"); found {
		n, err := dims.Uint64()
		if err != nil || n != 2 && n != 3 {
			return nil, errors.New("DESCRIBE: invalid dims")
		}
		schema.Dims = int(n)
	}
	if epoch, found := reply.Lookup("epoch"); found {
		if epoch.Kind != KindBulk || !ValidEpoch(string(epoch.Bulk)) {
			return nil, errors.New("DESCRIBE: invalid epoch")
		}
		schema.Epoch = string(epoch.Bulk)
		revision, found := reply.Lookup("revision")
		if !found {
			return nil, errors.New("DESCRIBE: no revision")
		}
		if revision.Kind != KindNull {
			n, err := revision.Uint64()
			if err != nil {
				return nil, errors.New("DESCRIBE: invalid revision")
			}
			schema.Revision = &n
		}
	}
	if columns, found := reply.Lookup("chunk_columns"); found {
		if columns.Kind != KindArray {
			return nil, errors.New("DESCRIBE: invalid chunk_columns")
		}
		var err error
		schema.ChunkColumns, err = ParseColumns(columns)
		if err != nil {
			return nil, err
		}
	}
	version, err := field("version", KindInteger)
	if err != nil {
		return nil, err
	}
	if schema.Version, err = version.Uint64(); err != nil {
		return nil, fmt.Errorf("DESCRIBE: version: %w", err)
	}
	for _, size := range []struct {
		key     string
		w, h, d *int
		limit   uint64
	}{{"chunk", &schema.ChunkWidth, &schema.ChunkHeight, &schema.ChunkDepth, 4096},
		{"large", &schema.LargeWidth, &schema.LargeHeight, &schema.LargeDepth, 1000000}} {
		pair, err := field(size.key, KindArray)
		if err != nil {
			return nil, err
		}
		axes, err := sizeAxes(pair, schema.Dims, size.limit)
		if err != nil {
			return nil, fmt.Errorf("DESCRIBE: %s: %w", size.key, err)
		}
		*size.w, *size.h, *size.d = axes[0], axes[1], axes[2]
	}
	if schema.BlockCount() == 0 {
		return nil, errors.New("DESCRIBE: chunk geometry exceeds the block limit")
	}
	options, err := field("options", KindMap)
	if err != nil {
		return nil, err
	}
	schema.Options = options.Map
	columns, err := field("columns", KindArray)
	if err != nil {
		return nil, err
	}
	if len(columns.Items) == 0 || len(columns.Items) > 1024 {
		return nil, errors.New("DESCRIBE: invalid block column count")
	}
	ids := map[uint32]bool{}
	for i, item := range columns.Items {
		column, err := ParseColumn(item)
		if err != nil {
			return nil, fmt.Errorf("DESCRIBE: column %d: %w", i+1, err)
		}
		if _, dup := schema.columnIndexes[column.Name]; dup {
			return nil, fmt.Errorf("DESCRIBE: column %s appears twice", column.Name)
		}
		if _, dup := ids[column.ID]; dup {
			return nil, fmt.Errorf("DESCRIBE: column id %d appears twice", column.ID)
		}
		ids[column.ID] = true
		schema.columnIndexes[column.Name] = len(schema.Columns)
		schema.Columns = append(schema.Columns, column)
	}
	return schema, nil
}

func sizeAxes(pair Value, dims int, limit uint64) ([3]int, error) {
	out := [3]int{1, 1, 1}
	if len(pair.Items) != dims {
		return out, fmt.Errorf("expected %d geometry axes", dims)
	}
	for i, item := range pair.Items {
		value, err := item.Uint64()
		if err != nil || value == 0 || value > limit {
			return out, fmt.Errorf("invalid size %q", item.Text)
		}
		out[i] = int(value)
	}
	return out, nil
}

// ParseColumn parses one DESCRIBE or WATCH schema column.
func ParseColumn(item Value) (Column, error) {
	if item.Kind != KindMap {
		return Column{}, fmt.Errorf("expected a map, got %s", item.Kind)
	}
	var column Column
	id, ok := item.Lookup("id")
	if !ok {
		return Column{}, errors.New("no id")
	}
	number, err := id.Uint64()
	if err != nil || number == 0 || number > math.MaxUint32 {
		return Column{}, fmt.Errorf("invalid id %q", id.Text)
	}
	column.ID = uint32(number)
	name, ok := item.Lookup("name")
	if !ok || name.Kind != KindBulk {
		return Column{}, errors.New("no name")
	}
	column.Name = string(name.Bulk)
	typeName, ok := item.Lookup("type")
	if !ok || typeName.Kind != KindBulk {
		return Column{}, fmt.Errorf("column %s has no type", column.Name)
	}
	if column.Type, err = ParseColumnType(string(typeName.Bulk)); err != nil {
		column.Type = ColumnType{Kind: ColumnUnknown, Name: string(typeName.Bulk)}
	}
	for _, flag := range []struct {
		key string
		dst *bool
	}{{"null", &column.Null}, {"required", &column.Required}} {
		value, ok := item.Lookup(flag.key)
		if !ok || value.Kind != KindBool {
			return Column{}, fmt.Errorf("column %s has no %s flag", column.Name, flag.key)
		}
		*flag.dst = value.Bool
	}
	if column.Default, ok = item.Lookup("default"); !ok {
		return Column{}, fmt.Errorf("column %s has no default", column.Name)
	}
	if column.Type.Kind == ColumnUnknown && column.Default.Kind != KindNull && column.Default.Kind != KindBulk {
		return Column{}, fmt.Errorf("column %s: unknown type default must be raw bytes", column.Name)
	}
	return column, nil
}

func ParseColumns(values Value) ([]Column, error) {
	if values.Kind != KindArray || len(values.Items) > 1024 {
		return nil, errors.New("expected column array")
	}
	columns := make([]Column, 0, len(values.Items))
	names := map[string]bool{}
	ids := map[uint32]bool{}
	for _, value := range values.Items {
		column, err := ParseColumn(value)
		if err != nil {
			return nil, err
		}
		if names[column.Name] || ids[column.ID] {
			return nil, errors.New("duplicate schema column")
		}
		names[column.Name] = true
		ids[column.ID] = true
		columns = append(columns, column)
	}
	return columns, nil
}
