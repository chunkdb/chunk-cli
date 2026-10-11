package main

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

type watchSchemaMetadata struct {
	dims         int
	chunkColumns []chunkclient.Column
}

func normalizeSchemaPush(push chunkclient.Value) (chunkclient.Value, watchSchemaMetadata, error) {
	metadata := watchSchemaMetadata{dims: 2}
	if len(push.Items) < 2 || push.Items[1].Kind != chunkclient.KindMap {
		return push, metadata, nil
	}
	m := push.Items[1]
	known := map[string]chunkclient.Value{}
	for _, key := range []string{"epoch", "revision", "schema_version", "dims", "columns", "chunk_columns"} {
		value, found := m.Lookup(key)
		if !found {
			return push, metadata, fmt.Errorf("WATCH: schema has no %s", key)
		}
		known[key] = value
	}
	dims, err := known["dims"].Uint64()
	if err != nil || dims != 2 && dims != 3 {
		return push, metadata, errors.New("WATCH: invalid schema dimensions")
	}
	metadata.dims = int(dims)
	metadata.chunkColumns, err = chunkclient.ParseColumns(known["chunk_columns"])
	if err != nil {
		return push, metadata, err
	}
	push.Items = []chunkclient.Value{push.Items[0], known["epoch"], known["revision"], known["schema_version"], known["columns"]}
	return push, metadata, nil
}

func nullableTxn(value chunkclient.Value) (any, error) {
	if value.Kind == chunkclient.KindNull {
		return nil, nil
	}
	n, err := value.Uint64()
	if err != nil {
		return nil, errors.New("WATCH: invalid transaction id")
	}
	return n, nil
}

func nullableTag(value chunkclient.Value) (any, error) {
	if value.Kind == chunkclient.KindNull {
		return nil, nil
	}
	if value.Kind != chunkclient.KindBulk || len(value.Bulk) > 256 {
		return nil, errors.New("WATCH: invalid tag")
	}
	typ := chunkclient.ColumnType{Kind: chunkclient.ColumnBytes}
	return jsonValue(value, &typ), nil
}

func printChunkPush(out *bytes.Buffer, entries chunkclient.Value, dims int) ([]any, error) {
	if entries.Kind != chunkclient.KindArray {
		return nil, errors.New("WATCH: invalid chunks")
	}
	result := make([]any, 0, len(entries.Items))
	for _, entry := range entries.Items {
		if entry.Kind != chunkclient.KindArray || len(entry.Items) < dims+2 {
			return nil, errors.New("WATCH: invalid chunk entry")
		}
		fields := orderedMap{}
		fmt.Fprint(out, "  chunk")
		for axis, key := range []string{"cx", "cy", "cz"}[:dims] {
			coordinate, err := entry.Items[axis].Int64()
			if err != nil {
				return nil, errors.New("WATCH: invalid chunk coordinate")
			}
			fields = append(fields, jsonField{key, coordinate})
			fmt.Fprintf(out, " %d", coordinate)
		}
		version, err := entry.Items[dims].Uint64()
		if err != nil {
			return nil, errors.New("WATCH: invalid chunk version")
		}
		form := entry.Items[dims+1]
		if form.Kind != chunkclient.KindBulk && form.Kind != chunkclient.KindNull {
			return nil, errors.New("WATCH: invalid chunk form")
		}
		typ := chunkclient.ColumnType{Kind: chunkclient.ColumnBytes}
		fields = append(fields, jsonField{"version", version}, jsonField{"form", jsonValue(form, &typ)})
		fmt.Fprintf(out, " version %d form %s\n", version, formatValue(form, &typ))
		result = append(result, fields)
	}
	return result, nil
}

func chunkChangesJSON(out *bytes.Buffer, entries chunkclient.Value, metadata watchSchemaMetadata) ([]any, error) {
	if entries.Kind != chunkclient.KindArray {
		return nil, errors.New("WATCH: invalid chunk changes")
	}
	result := make([]any, 0, len(entries.Items))
	for _, entry := range entries.Items {
		if entry.Kind != chunkclient.KindArray || len(entry.Items) < metadata.dims+2 {
			return nil, errors.New("WATCH: invalid chunk change")
		}
		fields := orderedMap{}
		var coordinateText bytes.Buffer
		for axis, key := range []string{"cx", "cy", "cz"}[:metadata.dims] {
			coordinate, err := entry.Items[axis].Int64()
			if err != nil {
				return nil, errors.New("WATCH: invalid chunk-change coordinate")
			}
			fields = append(fields, jsonField{key, coordinate})
			fmt.Fprintf(&coordinateText, " %d", coordinate)
		}
		before, beforeText, err := watchRow(entry.Items[metadata.dims], metadata.chunkColumns)
		if err != nil {
			return nil, err
		}
		after, afterText, err := watchRow(entry.Items[metadata.dims+1], metadata.chunkColumns)
		if err != nil {
			return nil, err
		}
		result = append(result, append(fields, jsonField{"before", before}, jsonField{"after", after}))
		fmt.Fprintf(out, "  chunk metadata%s: %s -> %s\n", coordinateText.String(), beforeText, afterText)
	}
	return result, nil
}

func columnsJSON(columns []chunkclient.Column) []any {
	result := make([]any, 0, len(columns))
	for _, column := range columns {
		result = append(result, orderedMap{{"id", column.ID}, {"name", column.Name}, {"type", column.Type.String()}, {"null", column.Null}, {"required", column.Required}, {"default", jsonValue(column.Default, &column.Type)}})
	}
	return result
}
