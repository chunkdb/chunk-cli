package chunkclient

import "testing"

func TestSectionedChunkFormLimit(t *testing.T) {
	schema, data := sectionedFixture(t)
	bound, err := SectionedChunkFormLimit(schema)
	if err != nil || bound < uint64(len(data)) {
		t.Fatal(bound, err)
	}
	schema.Options = []MapEntry{{Key: Value{Kind: KindBulk, Bulk: []byte("var_max_chunk_bytes")}, Value: Value{Kind: KindInteger, Text: "32"}}}
	bound, err = SectionedChunkFormLimit(schema)
	// Header, six directory entries, presence, two fixed bytes, one block
	// descriptor, two metadata descriptors with one fixed scalar, and quota.
	if err != nil || bound != 36+72+1+2+4+24+4+32+1+32 {
		t.Fatal(bound, err)
	}
	schema.ChunkColumns[0].Type = ColumnType{Kind: ColumnUnknown, Name: "future"}
	if _, err := SectionedChunkFormLimit(schema); err == nil {
		t.Fatal("unknown metadata accepted")
	}
	schema, _ = sectionedFixture(t)
	schema.ChunkDepth = 0
	if _, err := SectionedChunkFormLimit(schema); err == nil {
		t.Fatal("inactive depth accepted")
	}
}
