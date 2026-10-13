package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// Only the advertised binary SET CHUNK path has chunk-form admission.
// Preserve the original table token, including doubled identifier quotes.
func rawChunkTable(statement string) (string, bool) {
	words := rawChunkTokens(statement)
	if len(words) != 7 && len(words) != 10 || !strings.EqualFold(words[0], "SET") || !strings.EqualFold(words[1], "CHUNK") || !strings.EqualFold(words[4], "IN") {
		return "", false
	}
	for _, coordinate := range words[2:4] {
		if _, err := strconv.ParseInt(coordinate, 10, 64); err != nil {
			return "", false
		}
	}
	if !strings.HasPrefix(words[6], "$") {
		return "", false
	}
	index, err := strconv.ParseUint(words[6][1:], 10, 64)
	if err != nil || index != 1 {
		return "", false
	}
	if len(words) == 10 {
		if !strings.EqualFold(words[7], "IF") || !strings.EqualFold(words[8], "VERSION") {
			return "", false
		}
		if _, err := strconv.ParseUint(words[9], 10, 64); err != nil {
			return "", false
		}
	}
	return words[5], true
}

// CQL separates words, integer coordinates and $digit parameters without
// requiring spaces. Keep quoted identifiers intact; this splitter is local
// to the binary import shape and does not change ordinary classification.
func rawChunkTokens(statement string) []string {
	var result []string
	digit := func(c byte) bool { return c >= '0' && c <= '9' }
	word := func(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' }
	for _, token := range tokenize(statement) {
		if strings.HasPrefix(token, "\"") {
			result = append(result, token)
			continue
		}
		for len(token) > 0 {
			end := 1
			switch {
			case token[0] == '$', digit(token[0]), token[0] == '-' && len(token) > 1 && digit(token[1]):
				for end < len(token) && digit(token[end]) {
					end++
				}
			case word(token[0]):
				for end < len(token) && (word(token[end]) || digit(token[end])) {
					end++
				}
			default:
				return nil
			}
			result = append(result, token[:end])
			token = token[end:]
		}
	}
	return result
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
