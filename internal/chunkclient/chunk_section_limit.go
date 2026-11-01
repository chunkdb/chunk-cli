package chunkclient

import "fmt"

// SectionedChunkFormLimit bounds a known-schema binary mutation before reading it.
func SectionedChunkFormLimit(schema *Schema) (uint64, error) {
	if schema == nil || schema.BlockCount() == 0 || len(schema.Columns) > 1024 || len(schema.ChunkColumns) > 1024 {
		return 0, fmt.Errorf("SET CHUNK: invalid geometry or column count")
	}
	count := uint64(schema.BlockCount())
	presence := (count + 7) / 8
	var fixed, metadata, descriptors uint64
	for domain, columns := range [][]Column{schema.Columns, schema.ChunkColumns} {
		for _, column := range columns {
			if column.Type.Kind == ColumnUnknown {
				return 0, fmt.Errorf("SET CHUNK: cannot write unknown column type %s", column.Type)
			}
			if _, err := ParseColumnType(column.Type.String()); err != nil {
				return 0, err
			}
			if !column.Type.FixedWidth() {
				continue
			}
			if domain == 0 {
				fixed += (count*uint64(column.Type.Bits()) + 7) / 8
				if column.Null {
					fixed += presence
				}
				descriptors++
			} else {
				metadata += uint64((column.Type.Bits() + 7) / 8)
			}
		}
	}
	if fixed > 64*1024*1024 {
		return 0, fmt.Errorf("SET CHUNK: fixed payload exceeds its hard byte limit")
	}
	if len(schema.ChunkColumns) != 0 {
		metadata += 4 + 16*uint64(len(schema.ChunkColumns))
	}
	budget := uint64(maxSectionedVariableBytes)
	for _, option := range schema.Options {
		if option.Key.Kind == KindBulk && string(option.Key.Bulk) == "var_max_chunk_bytes" {
			value, err := option.Value.Uint64()
			if err != nil || value > maxSectionedVariableBytes {
				return 0, fmt.Errorf("SET CHUNK: invalid variable byte limit")
			}
			if value != 0 {
				budget = value
			}
		}
	}
	return 36 + 6*12 + presence + fixed + 4 + 24*descriptors + metadata + budget, nil
}
