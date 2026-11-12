package chunkclient

// ChunkFormLimit bounds a checked mutation before reading its payload.
func ChunkFormLimit(schema *Schema) (uint64, error) {
	return SectionedChunkFormLimit(schema)
}
