package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// The server has already parsed the statement. Read just the selector's
// arity, function list and grouping clause to check the returned values.
func validateAggregate(statement string, reply chunkclient.Value) error {
	tokens := replyTokens(statement)
	bad := func(detail string) error { return fmt.Errorf("AGGREGATE: %s", detail) }
	first := 2
	if tokenIs(tokens, first, "AROUND") {
		first++
	}
	i := first
	for i < len(tokens) {
		if _, err := strconv.ParseInt(tokens[i], 10, 64); err != nil {
			break
		}
		i++
	}
	dims := i - first
	for i < len(tokens) && !tokenIs(tokens, i, "FROM") {
		i++
	}
	if (dims != 2 && dims != 3) || i+2 >= len(tokens) {
		return bad("cannot determine the requested reply shape")
	}
	i += 2 // FROM and table name.
	var functions []string
	for i+3 < len(tokens) && tokens[i+1] == "(" && tokens[i+3] == ")" {
		functions = append(functions, strings.ToLower(tokens[i])+"("+tokens[i+2]+")")
		i += 4
		if i == len(tokens) || tokens[i] != "," {
			break
		}
		i++
	}
	if len(functions) == 0 {
		return bad("cannot determine the requested functions")
	}
	byChunk := false
	for ; i+1 < len(tokens); i++ {
		if tokenIs(tokens, i, "BY") && tokenIs(tokens, i+1, "CHUNK") {
			byChunk = true
			break
		}
	}
	if !byChunk {
		if reply.Kind != chunkclient.KindMap || len(reply.Map) != len(functions) {
			return bad("expected a map of the requested functions")
		}
		for i, entry := range reply.Map {
			if entry.Key.Kind != chunkclient.KindBulk || string(entry.Key.Bulk) != functions[i] || !validAggregateResult(functions[i], entry.Value) {
				return bad("invalid result for " + functions[i])
			}
		}
		return nil
	}
	if reply.Kind != chunkclient.KindArray {
		return bad("BY CHUNK expected an array")
	}
	var prev [3]int64
	for i, row := range reply.Items {
		if row.Kind != chunkclient.KindArray || len(row.Items) != dims+len(functions) {
			return bad("BY CHUNK row does not match the coordinate and function count")
		}
		point, err := scanPoint(row.Items[:dims])
		if err != nil || i > 0 && !areaCoordLess(prev, point, dims) {
			return bad("invalid or unordered BY CHUNK coordinate")
		}
		prev = point
		for i, function := range functions {
			if !validAggregateResult(function, row.Items[dims+i]) {
				return bad("invalid result for " + function)
			}
		}
	}
	return nil
}

func validAggregateResult(function string, value chunkclient.Value) bool {
	if function == "count(*)" {
		_, err := value.Uint64()
		return err == nil
	}
	if value.Kind == chunkclient.KindNull || value.Kind == chunkclient.KindDouble {
		return true
	}
	return !strings.HasPrefix(function, "avg(") && (value.Kind == chunkclient.KindInteger || value.Kind == chunkclient.KindBigNumber)
}
