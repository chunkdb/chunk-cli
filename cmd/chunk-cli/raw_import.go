package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// Only the advertised binary SET CHUNK path has chunk-form admission.
// Preserve the original table token, including doubled identifier quotes.
func rawChunkTable(statement string) (string, bool) {
	words := tokenize(statement)
	if len(words) < 7 || !strings.EqualFold(words[0], "SET") || !strings.EqualFold(words[1], "CHUNK") {
		return "", false
	}
	for i := 2; i+2 < len(words); i++ {
		if strings.EqualFold(words[i], "IN") && words[i+2] == "$1" {
			return words[i+1], true
		}
	}
	return "", false
}

func readChunkImport(client *chunkclient.Client, table, file string) ([]byte, error) {
	schema, err := client.Describe(table)
	if err != nil {
		return nil, err
	}
	limit, err := chunkclient.ChunkFormLimit(schema)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Read at most one byte past the schema bound, without narrowing a u64
	// limit to a negative int64 or changing any admitted payload byte.
	readLimit := int64(math.MaxInt64)
	if limit < math.MaxInt64 {
		readLimit = int64(limit + 1)
	}
	data, err := io.ReadAll(io.LimitReader(f, readLimit))
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) > limit {
		return nil, fmt.Errorf("SET CHUNK: form exceeds schema size bound %d bytes", limit)
	}
	return data, nil
}
