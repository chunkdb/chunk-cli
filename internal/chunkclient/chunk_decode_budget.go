package chunkclient

import (
	"fmt"
	"math"
	"math/bits"
	"unsafe"
)

// DecodeLimitError refuses local materialization of a completely framed form.
type DecodeLimitError struct{ Detail string }

func (e *DecodeLimitError) Error() string { return e.Detail }

// ChunkDecodeArrayBytes bounds retained decode arrays without materializing cells.
// Scalar contents and the complete typed form are still checked by the decoder.
func ChunkDecodeArrayBytes(schema *Schema, columns []string, data []byte, values bool) (uint64, error) {
	if schema == nil || schema.BlockCount() == 0 || len(schema.Columns) > 1024 || len(schema.ChunkColumns) > 1024 || len(columns) > len(schema.Columns) {
		return 0, fmt.Errorf("GET AREA: invalid geometry or column count")
	}
	form, err := parseSectionedForm(data, math.MaxUint32, false)
	if err != nil {
		return 0, err
	}
	if form.header.schema != schema.Version {
		return 0, &ServerError{Code: "SCHEMA_MISMATCH", Keys: map[string]string{"actual": fmt.Sprint(form.header.schema), "current": fmt.Sprint(schema.Version), "table": schema.Table}, Message: "re-read schema before decoding values"}
	}
	count := uint64(schema.BlockCount())
	presence := form.section(sectionPresence)
	if uint64(len(presence)) != (count+7)/8 || count%8 != 0 && presence[len(presence)-1]>>uint(count%8) != 0 {
		return 0, fmt.Errorf("GET AREA: invalid presence shape")
	}
	selected := uint64(len(schema.Columns))
	if len(columns) != 0 {
		selected = uint64(len(columns))
	}
	// Counts and column cardinality are admitted above, so every product fits
	// uint64 even at the maximum geometry on a 64-bit host.
	bytes := uint64(unsafe.Sizeof(Chunk{})) + uint64(len(schema.ChunkColumns))*uint64(unsafe.Sizeof(Value{}))
	if len(columns) != 0 {
		bytes += selected * uint64(unsafe.Sizeof(Column{}))
	}
	if values {
		var present uint64
		for _, value := range presence {
			present += uint64(bits.OnesCount8(value))
		}
		bytes += count * uint64(unsafe.Sizeof(bool(false)))
		if present != 0 {
			bytes += count*uint64(unsafe.Sizeof(uint32(0))) + present*uint64(unsafe.Sizeof(int(0)))
		}
		bytes += selected*uint64(unsafe.Sizeof([]Value{})) + selected*present*uint64(unsafe.Sizeof(Value{}))
	}
	return bytes, nil
}
