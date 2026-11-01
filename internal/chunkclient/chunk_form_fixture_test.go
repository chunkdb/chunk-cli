package chunkclient

import (
	"encoding/binary"
	"testing"
)

// checkedPackedForm assembles independently specified packed sections into CKCF.
func checkedPackedForm(t *testing.T, schema *Schema, names []string, version uint64, packed []byte) []byte {
	t.Helper()
	columns := schema.Columns
	if len(names) != 0 {
		columns = nil
		for _, name := range names {
			c, ok := schema.Column(name)
			if !ok {
				t.Fatal(name)
			}
			columns = append(columns, c)
		}
	}
	count := schema.BlockCount()
	mask := (count + 7) / 8
	presence, packed := packed[:mask], packed[mask:]
	var fixed, validity, descriptors []byte
	for _, c := range columns {
		if !c.Type.FixedWidth() {
			continue
		}
		width := c.Type.Bits()
		size := (count*width + 7) / 8
		nullOffset, nullSize := uint32(0), uint32(0)
		if c.Null {
			nullOffset, nullSize = uint32(len(validity)), uint32(mask)
		}
		for _, v := range []uint32{c.ID, uint32(width), uint32(len(fixed)), uint32(size), nullOffset, nullSize} {
			descriptors = binary.LittleEndian.AppendUint32(descriptors, v)
		}
		fixed = append(fixed, packed[:size]...)
		packed = packed[size:]
		if c.Null {
			validity = append(validity, packed[:mask]...)
			packed = packed[mask:]
		}
	}
	descriptors = append(binary.LittleEndian.AppendUint32(nil, uint32(len(descriptors)/24)), descriptors...)
	kinds := []uint16{sectionPresence}
	bodies := [][]byte{presence}
	for _, entry := range []struct {
		kind uint16
		body []byte
	}{{sectionFixed, fixed}, {sectionValidity, validity}, {sectionVariable, packed}, {sectionBlockSpans, descriptors}} {
		if len(entry.body) != 0 {
			kinds = append(kinds, entry.kind)
			bodies = append(bodies, entry.body)
		}
	}
	header := make([]byte, 36)
	copy(header, "CKCF")
	header[4] = 1
	if len(names) != 0 {
		header[6] = 1
	}
	binary.LittleEndian.PutUint64(header[8:], version)
	binary.LittleEndian.PutUint64(header[16:], schema.Version)
	return sectionedTestForm(header, kinds, bodies)
}

func (b *chunkFormBuilder) checked(t *testing.T, schema *Schema, names []string) []byte {
	t.Helper()
	data := b.Bytes()
	return checkedPackedForm(t, schema, names, binary.LittleEndian.Uint64(data), data[16:])
}
