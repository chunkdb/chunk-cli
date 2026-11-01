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
	shape, ok := rawChunkShapeOf(statement)
	return shape.table, ok
}

type rawChunkShape struct {
	table   string
	columns []string
	dims    int
}

func rawChunkShapeOf(statement string) (rawChunkShape, bool) {
	words := rawChunkTokens(statement)
	bad := func() (rawChunkShape, bool) { return rawChunkShape{}, false }
	if len(words) < 7 || !strings.EqualFold(words[0], "SET") || !strings.EqualFold(words[1], "CHUNK") {
		return bad()
	}
	i := 2
	for i < len(words) {
		if _, err := strconv.ParseInt(words[i], 10, 64); err != nil {
			break
		}
		i++
	}
	shape := rawChunkShape{dims: i - 2}
	if shape.dims != 2 && shape.dims != 3 || i+2 >= len(words) || !strings.EqualFold(words[i], "IN") {
		return bad()
	}
	shape.table = words[i+1]
	i += 2
	if strings.EqualFold(words[i], "COLUMNS") {
		i++
		for i < len(words) && !strings.HasPrefix(words[i], "$") {
			name := words[i]
			if strings.HasPrefix(name, "\"") {
				name = strings.ReplaceAll(name[1:len(name)-1], "\"\"", "\"")
			}
			shape.columns = append(shape.columns, name)
			i++
		}
		if len(shape.columns) == 0 {
			return bad()
		}
	}
	if i >= len(words) || !strings.HasPrefix(words[i], "$") {
		return bad()
	}
	index, err := strconv.ParseUint(words[i][1:], 10, 64)
	if err != nil || index != 1 {
		return bad()
	}
	i++
	if i == len(words) {
		return shape, true
	}
	if i+2 == len(words) && strings.EqualFold(words[i], "IF") && strings.EqualFold(words[i+1], "ABSENT") {
		return shape, true
	}
	if i+3 == len(words) {
		if !strings.EqualFold(words[i], "IF") || !strings.EqualFold(words[i+1], "VERSION") {
			return bad()
		}
		// Core integer parsing canonicalizes negative zero before its
		// unsigned check. Preserve the suffix bytes sent to the server.
		version, err := strconv.ParseUint(strings.TrimPrefix(words[i+2], "-"), 10, 64)
		if err != nil || strings.HasPrefix(words[i+2], "-") && version != 0 {
			return bad()
		}
		return shape, true
	}
	return bad()
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

func readChunkImport(client *chunkclient.Client, table, file string, columns ...string) ([]byte, error) {
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
	if err := chunkclient.ValidateSectionedChunkMutation(schema, columns, data); err != nil {
		return nil, err
	}
	return data, nil
}
