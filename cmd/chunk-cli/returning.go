package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// classifyReturning recognizes SET BLOCK and DELETE BLOCK with RETURNING,
// whose rows are decoded by the table's columns.
func classifyReturning(tokens []string) statementInfo {
	keyword := func(i int) string {
		if i < len(tokens) {
			return strings.ToUpper(tokens[i])
		}
		return ""
	}
	target := "IN"
	if keyword(0) == "DELETE" {
		target = "FROM"
	}
	table := -1
	for i := 4; i < 6 && i < len(tokens); i++ {
		if keyword(i) == target {
			table = i + 1
			break
		}
	}
	if table < 0 || table >= len(tokens) {
		return statementInfo{}
	}
	for at := len(tokens) - 2; at > table; at-- {
		if keyword(at) != "RETURNING" || tokens[at-1] == "," {
			continue
		}
		rest := tokens[at+1:]
		if strings.EqualFold(rest[0], "OLD") || strings.EqualFold(rest[0], "NEW") {
			rest = rest[1:]
		}
		info := statementInfo{kind: statementReturning, table: tokens[table]}
		if len(rest) == 1 && rest[0] == "*" {
			return info
		}
		columns, ok := readColumnNames(rest)
		if ok {
			info.columns = columns
			return info
		}
	}
	return statementInfo{}
}

// printReturning prints {applied, version, rows} with typed values.
func printReturning(w io.Writer, schema *chunkclient.Schema, info statementInfo, reply chunkclient.Value, opts statementOptions) error {
	bad := func(why string) error { return fmt.Errorf("RETURNING: %s", why) }
	columns, err := blockColumns(schema, info.columns)
	if err != nil {
		return err
	}
	applied, haveApplied := reply.Lookup("applied")
	version, haveVersion := reply.Lookup("version")
	rows, haveRows := reply.Lookup("rows")
	if reply.Kind != chunkclient.KindMap || !haveApplied || applied.Kind != chunkclient.KindBool || !haveVersion || !haveRows || rows.Kind != chunkclient.KindArray {
		return bad("expected applied, version and rows")
	}
	var versionValue any
	if version.Kind != chunkclient.KindNull {
		n, err := version.Uint64()
		if err != nil {
			return bad("invalid version")
		}
		versionValue = n
	}
	if !applied.Bool && len(rows.Items) != 0 {
		return bad("a refused write returned rows")
	}
	dims := schema.Dims
	if dims == 0 {
		dims = 2
	}
	var out bytes.Buffer
	fmt.Fprintf(&out, "applied = %t\nversion = ", applied.Bool)
	if versionValue == nil {
		out.WriteString("NULL\n")
	} else {
		fmt.Fprintf(&out, "%d\n", versionValue)
	}
	list := make([]any, 0, len(rows.Items))
	for _, row := range rows.Items {
		if row.Kind != chunkclient.KindArray || len(row.Items) != dims+len(columns) {
			return bad("a row does not match the columns (the table may have changed)")
		}
		fields := orderedMap{}
		out.WriteString("row")
		for axis := range dims {
			n, err := row.Items[axis].Int64()
			if err != nil {
				return bad("invalid coordinate")
			}
			fields = append(fields, jsonField{[]string{"x", "y", "z"}[axis], n})
			fmt.Fprintf(&out, " %d", n)
		}
		out.WriteByte('\n')
		for i, column := range columns {
			value := row.Items[dims+i]
			if err := validateWatchValue(value, column); err != nil {
				return bad(err.Error())
			}
			fields = append(fields, jsonField{column.Name, jsonValue(value, &column.Type)})
			fmt.Fprintf(&out, "  %s = %s\n", column.Name, formatValue(value, &column.Type))
		}
		list = append(list, fields)
	}
	if opts.json {
		return writeJSON(w, orderedMap{{"applied", applied.Bool}, {"version", versionValue}, {"rows", list}})
	}
	_, err = w.Write(out.Bytes())
	return err
}
