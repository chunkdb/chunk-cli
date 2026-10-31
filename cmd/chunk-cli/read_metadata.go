package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// Metadata precedes the row/entries in the requested, fixed prefix order.
func printReadMetadata(w io.Writer, schema *chunkclient.Schema, info statementInfo, reply chunkclient.Value, opts statementOptions) error {
	count := 1
	if info.withVersion {
		count++
	}
	if info.withSchema {
		count++
	}
	if reply.Kind != chunkclient.KindArray || len(reply.Items) < count {
		return errors.New("GET: invalid metadata reply")
	}
	fields := orderedMap{}
	var out bytes.Buffer
	i := 0
	if info.withVersion {
		var version any
		if reply.Items[i].Kind != chunkclient.KindNull {
			n, err := reply.Items[i].Uint64()
			if err != nil {
				return fmt.Errorf("GET: invalid version: %w", err)
			}
			version = n
		}
		fields = append(fields, jsonField{"version", version})
		fmt.Fprintf(&out, "version = %s\n", formatValue(reply.Items[i], nil))
		i++
	}
	if info.withSchema {
		version, err := reply.Items[i].Uint64()
		if err != nil {
			return fmt.Errorf("GET: invalid schema version: %w", err)
		}
		if version != schema.Version {
			return &chunkclient.ServerError{Code: "SCHEMA_MISMATCH", Keys: map[string]string{"expected": fmt.Sprint(schema.Version), "actual": fmt.Sprint(version)}, Message: "re-read schema before decoding values"}
		}
		fields = append(fields, jsonField{"schema_version", version})
		fmt.Fprintf(&out, "schema_version = %d\n", version)
		i++
	}
	var values bytes.Buffer
	var err error
	key := "values"
	if info.kind == statementGetBlock {
		err = printBlock(&values, schema, info.columns, reply.Items[i], opts)
	} else {
		key = "entries"
		err = printArea(&values, schema, info.columns, reply.Items[i], opts)
	}
	if err != nil {
		return err
	}
	if opts.json {
		decoder := json.NewDecoder(&values)
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		return writeJSON(w, append(fields, jsonField{key, value}))
	}
	out.Write(values.Bytes())
	_, err = w.Write(out.Bytes())
	return err
}
