package chunkclient

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"unicode/utf8"
)

const maxSectionedVariableBytes = 64 * 1024 * 1024

// DecodeSectionedChunkForm reads the checked form independently of legacy dispatch.
func DecodeSectionedChunkForm(schema *Schema, columns []string, data []byte) (*Chunk, error) {
	return decodeSectionedChunkForm(schema, columns, data, false)
}

// ValidateSectionedChunkMutation refuses unknown data before binary import.
// Selected block-column imports may omit the preserved chunk metadata section.
func ValidateSectionedChunkMutation(schema *Schema, columns []string, data []byte) error {
	if schema == nil {
		return fmt.Errorf("SET CHUNK: missing schema")
	}
	for _, domain := range [][]Column{schema.Columns, schema.ChunkColumns} {
		for _, column := range domain {
			if column.Type.Kind == ColumnUnknown {
				return fmt.Errorf("SET CHUNK: unknown column type %s", column.Type)
			}
		}
	}
	if _, err := parseSectionedForm(data, math.MaxUint32, true); err != nil {
		return err
	}
	_, err := decodeSectionedChunkForm(schema, columns, data, len(columns) != 0)
	return err
}

func decodeSectionedChunkForm(schema *Schema, columns []string, data []byte, preserveMetadata bool) (*Chunk, error) {
	bad := func(what string) (*Chunk, error) { return nil, fmt.Errorf("chunk form: %s", what) }
	if schema == nil || schema.BlockCount() == 0 || len(schema.Columns) > 1024 || len(schema.ChunkColumns) > 1024 {
		return bad("invalid geometry or column count")
	}
	count := schema.BlockCount()
	form, err := parseSectionedForm(data, math.MaxUint32, false)
	if err != nil {
		return nil, err
	}
	if form.header.schema != schema.Version {
		return bad("schema version mismatch")
	}
	if form.header.flags&chunkFormProjected != 0 && len(columns) == 0 {
		return bad("projection flag mismatch")
	}
	selected := schema.Columns
	if len(columns) != 0 {
		selected = make([]Column, 0, len(columns))
		for _, name := range columns {
			column, found := schema.Column(name)
			if !found {
				return bad("unknown projected column " + name)
			}
			selected = append(selected, column)
		}
		if form.header.flags&chunkFormProjected == 0 {
			if len(selected) != len(schema.Columns) {
				return bad("full form read as a partial projection")
			}
			for i := range selected {
				if selected[i].ID != schema.Columns[i].ID {
					return bad("full form read in another column order")
				}
			}
		}
	}
	spans, err := parseFormBlockSpans(form, count)
	if err != nil {
		return nil, err
	}
	if len(form.section(sectionFixed))+len(form.section(sectionValidity)) > 64*1024*1024 {
		return bad("fixed payload exceeds its byte limit")
	}
	byID := make(map[uint32]formBlockSpan, len(spans))
	for _, span := range spans {
		byID[span.id] = span
	}
	seen, names := map[uint32]bool{}, map[string]bool{}
	variables := make(map[uint32]int)
	consumed := 0
	for index, column := range selected {
		if column.Type.Kind != ColumnUnknown {
			if _, err := ParseColumnType(column.Type.String()); err != nil {
				return bad("invalid block column type")
			}
		}
		if column.ID == 0 || seen[column.ID] || names[column.Name] {
			return bad("duplicate column ID or name")
		}
		seen[column.ID], names[column.Name] = true, true
		span, described := byID[column.ID]
		fixed := column.Type.FixedWidth()
		if column.Type.Kind == ColumnUnknown {
			fixed = described
		}
		if fixed {
			if !described || (column.Type.Kind != ColumnUnknown && span.width != column.Type.Bits()) || (len(span.validity) != 0) != column.Null {
				return bad("fixed descriptor differs from schema")
			}
			if consumed >= len(spans) || spans[consumed].id != column.ID {
				return bad("fixed descriptor order differs from projection")
			}
			consumed++
		} else {
			if described {
				return bad("variable column has a fixed descriptor")
			}
			variables[column.ID] = index
		}
	}
	if consumed != len(spans) {
		return bad("unexpected fixed descriptors")
	}
	metadata, err := parseFormChunkColumns(form)
	if err != nil {
		return nil, err
	}
	omittedMetadata := preserveMetadata && form.section(sectionChunkColumns) == nil
	if !omittedMetadata && len(metadata) != len(schema.ChunkColumns) {
		return bad("chunk metadata differs from schema")
	}
	variable := form.section(sectionVariable)
	if len(variable) > maxSectionedVariableBytes {
		return bad("variable data exceeds its hard byte limit")
	}
	depth := 1
	if schema.Dims == 3 {
		depth = schema.ChunkDepth
	}
	chunk := &Chunk{Version: form.header.version, SchemaVersion: form.header.schema, CommitTimeMS: form.header.commitTime,
		Projected: form.header.flags&chunkFormProjected != 0, Private: form.header.flags&chunkFormPrivate != 0,
		Width: schema.ChunkWidth, Height: schema.ChunkHeight, Depth: depth, Columns: selected,
		Present: make([]bool, count), Values: make([][]Value, len(selected))}
	presence := form.section(sectionPresence)
	for i := range count {
		chunk.Present[i] = bit(presence, i)
	}
	for index, column := range selected {
		values := make([]Value, count)
		span, fixed := byID[column.ID]
		for i := range count {
			values[i] = Value{Kind: KindNull}
			if !chunk.Present[i] {
				continue
			}
			if !fixed {
				if !column.Null {
					values[i] = Value{Kind: KindBulk, Bulk: []byte{}}
				}
				continue
			}
			if column.Null && !bit(span.validity, i) {
				continue
			}
			if column.Type.Kind == ColumnUnknown {
				values[i] = Value{Kind: KindBulk, Bulk: extractBits(span.values, i*span.width, span.width)}
			} else {
				values[i] = fixedValue(column.Type, span.values, i*span.width)
			}
		}
		chunk.Values[index] = values
	}
	var lastID, lastBlock uint32
	first := true
	for offset := 0; offset < len(variable); {
		if len(variable)-offset < 12 {
			return bad("truncated variable entry")
		}
		id, block, length := binary.LittleEndian.Uint32(variable[offset:]), binary.LittleEndian.Uint32(variable[offset+4:]), uint64(binary.LittleEndian.Uint32(variable[offset+8:]))
		offset += 12
		if length > uint64(len(variable)-offset) {
			return bad("truncated variable value")
		}
		index, found := variables[id]
		if !found || uint64(block) >= uint64(count) || !chunk.Present[int(block)] || (!first && (id < lastID || (id == lastID && block <= lastBlock))) {
			return bad("unordered, unknown or absent variable entry")
		}
		column := selected[index]
		value := variable[offset : offset+int(length)]
		offset += int(length)
		if (length == 0 && !column.Null) || (column.Type.Kind != ColumnUnknown && length > uint64(column.Type.Size)) || (column.Type.Kind == ColumnText && !utf8.Valid(value)) {
			return bad("invalid variable value")
		}
		chunk.Values[index][int(block)] = Value{Kind: KindBulk, Bulk: bytes.Clone(value)}
		lastID, lastBlock, first = id, block, false
	}
	metaSchema := make(map[uint32]int, len(schema.ChunkColumns))
	metaNames := make(map[string]bool, len(schema.ChunkColumns))
	for index, column := range schema.ChunkColumns {
		if column.Type.Kind != ColumnUnknown {
			if _, err := ParseColumnType(column.Type.String()); err != nil {
				return bad("invalid chunk column type")
			}
		}
		if column.ID == 0 || metaNames[column.Name] {
			return bad("invalid chunk metadata ID or name")
		}
		if _, duplicate := metaSchema[column.ID]; duplicate {
			return bad("duplicate chunk metadata ID")
		}
		metaSchema[column.ID], metaNames[column.Name] = index, true
	}
	if !omittedMetadata {
		chunk.MetadataColumns = schema.ChunkColumns
		chunk.MetadataValues = make([]Value, len(metadata))
	}
	variableBytes := uint64(len(variable))
	for _, entry := range metadata {
		index, found := metaSchema[entry.id]
		if !found {
			return bad("unknown chunk metadata ID")
		}
		column := schema.ChunkColumns[index]
		value, err := decodeSectionedMetadata(column, entry.value, entry.null)
		if err != nil {
			return nil, err
		}
		chunk.MetadataValues[index] = value
		if !column.Type.FixedWidth() && !entry.null && (column.Null || len(entry.value) != 0) {
			variableBytes += 12 + uint64(len(entry.value))
		}
	}
	if variableBytes > maxSectionedVariableBytes {
		return bad("block and chunk values exceed the combined variable-byte limit")
	}
	for _, section := range form.sections {
		if section.kind >= sectionPresence && section.kind <= sectionBlockSpans {
			continue
		}
		if chunk.UnknownSections == nil {
			chunk.UnknownSections = make(map[uint16][]byte)
		}
		chunk.UnknownSections[section.kind] = bytes.Clone(section.body)
	}
	return chunk, nil
}

func decodeSectionedMetadata(column Column, raw []byte, null bool) (Value, error) {
	bad := func() (Value, error) { return Value{}, fmt.Errorf("invalid chunk metadata value for %s", column.Name) }
	if null {
		if !column.Null {
			return bad()
		}
		return Value{Kind: KindNull}, nil
	}
	if column.Type.Kind == ColumnUnknown {
		return Value{Kind: KindBulk, Bulk: bytes.Clone(raw)}, nil
	}
	if column.Type.FixedWidth() {
		if !validArrayPadding(raw, uint64(column.Type.Bits())) {
			return bad()
		}
		return fixedValue(column.Type, raw, 0), nil
	}
	if len(raw) > column.Type.Size || (column.Type.Kind == ColumnText && !utf8.Valid(raw)) {
		return bad()
	}
	return Value{Kind: KindBulk, Bulk: bytes.Clone(raw)}, nil
}
