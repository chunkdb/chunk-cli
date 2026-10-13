package chunkclient

import (
	"fmt"
	"math"
)

// ChunkFormLimit is the core layout bound: versions, presence, fixed and
// nullable bit sections, then the advertised variable-byte budget.
func ChunkFormLimit(schema *Schema) (uint64, error) {
	if schema.Dims != 0 && schema.Dims != 2 {
		return 0, fmt.Errorf("SET CHUNK: unsupported chunk form dimensions %d", schema.Dims)
	}
	if schema.ChunkWidth <= 0 || schema.ChunkHeight <= 0 {
		return 0, fmt.Errorf("SET CHUNK: invalid chunk dimensions")
	}
	mul := func(a, b uint64) (uint64, error) {
		if b != 0 && a > math.MaxUint64/b {
			return 0, fmt.Errorf("SET CHUNK: schema size overflows uint64")
		}
		return a * b, nil
	}
	add := func(a, b uint64) (uint64, error) {
		if a > math.MaxUint64-b {
			return 0, fmt.Errorf("SET CHUNK: schema size overflows uint64")
		}
		return a + b, nil
	}
	blocks, err := mul(uint64(schema.ChunkWidth), uint64(schema.ChunkHeight))
	if err != nil {
		return 0, err
	}
	round, err := add(blocks, 7)
	if err != nil {
		return 0, err
	}
	mask := round / 8
	limit, err := add(formHeaderBytes, mask)
	if err != nil {
		return 0, err
	}
	variable := false
	for _, column := range schema.Columns {
		if column.Type.Kind < ColumnUnsigned || column.Type.Kind > ColumnBytes {
			return 0, fmt.Errorf("SET CHUNK: column %s has unknown type %s; cannot bound binary import", column.Name, column.Type)
		}
		if !column.Type.FixedWidth() {
			variable = true
			continue
		}
		if column.Type.Bits() <= 0 {
			return 0, fmt.Errorf("SET CHUNK: column %s has invalid width for type %s", column.Name, column.Type)
		}
		bits, err := mul(blocks, uint64(column.Type.Bits()))
		if err != nil {
			return 0, err
		}
		bits, err = add(bits, 7)
		if err != nil {
			return 0, err
		}
		limit, err = add(limit, bits/8)
		if err != nil {
			return 0, err
		}
		if column.Null {
			limit, err = add(limit, mask)
			if err != nil {
				return 0, err
			}
		}
	}
	var budget uint64
	found := false
	for _, option := range schema.Options {
		if option.Key.Kind == KindBulk && string(option.Key.Bulk) == "var_max_chunk_bytes" {
			budget, err = option.Value.Uint64()
			if err != nil {
				return 0, fmt.Errorf("SET CHUNK: invalid var_max_chunk_bytes: %w", err)
			}
			found = true
		}
	}
	if variable && !found {
		return 0, fmt.Errorf("SET CHUNK: DESCRIBE has no var_max_chunk_bytes to bound variable columns")
	}
	return add(limit, budget)
}
