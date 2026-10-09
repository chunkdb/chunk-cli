package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

type watchPosition struct {
	Epoch    string `json:"epoch"`
	Revision uint64 `json:"revision"`
}
type watchOptions struct {
	table     string
	statement string
	json      bool
}

func parseWatchPosition(text string) (watchPosition, error) {
	epoch, digits, ok := strings.Cut(text, ":")
	if !ok || len(epoch) != 32 || strings.Trim(epoch, "0123456789abcdefABCDEF") != "" || digits == "" || strings.Trim(digits, "0123456789") != "" {
		return watchPosition{}, fmt.Errorf("invalid position %q: expected 32 hex digits:revision", text)
	}
	rev, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return watchPosition{}, fmt.Errorf("invalid revision: %w", err)
	}
	return watchPosition{strings.ToLower(epoch), rev}, nil
}

func parseWatchArgs(args []string, json bool) (watchOptions, error) {
	if len(args) == 0 {
		return watchOptions{}, errors.New("watch needs a table")
	}
	table := args[0]
	if table == "" || (table[0] != '_' && (table[0] < 'a' || table[0] > 'z')) || strings.Trim(table, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return watchOptions{}, fmt.Errorf("invalid table %q", table)
	}
	opts := watchOptions{table: table, statement: "WATCH " + table, json: json}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var area, after string
	fs.StringVar(&area, "area", "", "inclusive chunk rectangle")
	fs.StringVar(&after, "after", "", "epoch:revision")
	fs.BoolVar(&opts.json, "json", json, "one JSON object per event")
	if err := fs.Parse(args[1:]); err != nil {
		return watchOptions{}, err
	}
	if fs.NArg() != 0 {
		return watchOptions{}, fmt.Errorf("unexpected watch argument %q", fs.Arg(0))
	}
	if area != "" {
		parts := strings.Split(area, ",")
		var xy [4]int64
		if len(parts) != 4 {
			return watchOptions{}, errors.New("--area needs cx0,cy0,cx1,cy1")
		}
		for i, part := range parts {
			n, err := strconv.ParseInt(part, 10, 64)
			if err != nil {
				return watchOptions{}, fmt.Errorf("--area: %w", err)
			}
			xy[i] = n
		}
		if xy[0] > xy[2] || xy[1] > xy[3] {
			return watchOptions{}, errors.New("--area bounds are reversed")
		}
		opts.statement += fmt.Sprintf(" AREA %d %d TO %d %d", xy[0], xy[1], xy[2], xy[3])
	}
	if after != "" {
		p, err := parseWatchPosition(after)
		if err != nil {
			return watchOptions{}, err
		}
		opts.statement += fmt.Sprintf(" AFTER %s %d", p.Epoch, p.Revision)
	}
	return opts, nil
}

// runWatch owns the stream reader. Cancellation sends UNWATCH concurrently;
// this reader drains every preceding push before consuming its acknowledgement.
func runWatch(ctx context.Context, opts globalOptions, watch watchOptions, term console, stdout io.Writer) error {
	if opts.Statement.blocks || opts.Statement.in != "" || opts.Statement.out != "" || opts.Statement.newPasswordFile != "" {
		return errors.New("watch supports --json; statement file and --blocks options do not apply")
	}
	uri, err := chunkuri.Parse(opts.URI)
	if err != nil {
		return err
	}
	login, err := loginFor(opts, uri, term)
	if err != nil {
		return err
	}
	stream, err := connectLogin(opts, uri, login)
	if err != nil {
		return err
	}
	defer stream.Close()
	describe, err := connectLogin(opts, uri, login)
	if err != nil {
		return err
	}
	defer describe.Close()
	initial, err := describe.Describe(watch.table)
	if err != nil {
		return err
	}
	schemas := map[uint64][]chunkclient.Column{initial.Version: initial.Columns}
	start, err := stream.BeginWatch(watch.statement)
	if err != nil {
		return err
	}
	words := strings.Fields(start)
	if len(words) != 3 || words[0] != "OK" {
		return fmt.Errorf("WATCH: invalid start position %q", start)
	}
	position, err := parseWatchPosition(words[1] + ":" + words[2])
	if err != nil {
		return err
	}
	if watch.json {
		err = writeJSON(stdout, orderedMap{{"type", "start"}, {"position", position}})
	} else {
		_, err = fmt.Fprintf(stdout, "start %s:%d\n", position.Epoch, position.Revision)
	}
	if err != nil {
		return err
	}
	done := make(chan struct{})
	stopped := make(chan error, 1)
	go func() {
		select {
		case <-ctx.Done():
			stopped <- stream.EndWatch()
		case <-done:
			stopped <- nil
		}
	}()
	defer func() { close(done); <-stopped }()
	for {
		push, err := stream.ReadWatch()
		if err != nil {
			return err
		}
		if push.Kind == chunkclient.KindSimple && push.Text == "OK" && ctx.Err() != nil {
			return nil
		}
		if push.Kind != chunkclient.KindPush {
			return fmt.Errorf("WATCH: expected a push, got %s", push.Kind)
		}
		if err := printWatchEvent(stdout, push, watch.json, schemas, func(version uint64) ([]chunkclient.Column, error) {
			schema, err := describe.Describe(watch.table)
			if err != nil {
				return nil, err
			}
			if schema.Version != version {
				return nil, fmt.Errorf("WATCH: need schema version %d, DESCRIBE returned %d", version, schema.Version)
			}
			return schema.Columns, nil
		}); err != nil {
			return err
		}
	}
}

func watchString(v chunkclient.Value) (string, error) {
	if v.Kind != chunkclient.KindBulk {
		return "", fmt.Errorf("WATCH: expected bulk string, got %s", v.Kind)
	}
	return string(v.Bulk), nil
}

// watchCoordinate preserves the protocol's [chunk, offset] representation
// when an absolute block coordinate does not fit int64.
func watchCoordinate(v chunkclient.Value) (any, string, error) {
	if v.Kind == chunkclient.KindInteger {
		n, err := v.Int64()
		return n, v.Text, err
	}
	if v.Kind != chunkclient.KindArray || len(v.Items) != 2 {
		return nil, "", errors.New("WATCH: invalid block coordinate")
	}
	chunk, err := v.Items[0].Int64()
	if err != nil {
		return nil, "", err
	}
	offset, err := v.Items[1].Uint64()
	if err != nil {
		return nil, "", err
	}
	return []any{chunk, offset}, fmt.Sprintf("[%d,%d]", chunk, offset), nil
}

func watchRow(row chunkclient.Value, columns []chunkclient.Column) (any, string, error) {
	if row.Kind == chunkclient.KindNull {
		return nil, "(absent)", nil
	}
	if row.Kind != chunkclient.KindArray || len(row.Items) != len(columns) {
		return nil, "", errors.New("WATCH: row does not match its schema")
	}
	values := orderedMap{}
	literals := make([]string, len(columns))
	for i, column := range columns {
		v := row.Items[i]
		if err := validateWatchValue(v, column); err != nil {
			return nil, "", err
		}
		values = append(values, jsonField{column.Name, jsonValue(v, &column.Type)})
		literals[i] = column.Name + " = " + formatValue(v, &column.Type)
	}
	return values, "{" + strings.Join(literals, ", ") + "}", nil
}

func validateWatchValue(v chunkclient.Value, column chunkclient.Column) error {
	valid := false
	switch {
	case v.Kind == chunkclient.KindNull:
		valid = column.Null
	case column.Type.Kind == chunkclient.ColumnUnsigned:
		_, err := v.Uint64()
		valid = err == nil
	case column.Type.Kind == chunkclient.ColumnSigned:
		_, err := v.Int64()
		valid = err == nil
	case column.Type.Kind == chunkclient.ColumnBool:
		valid = v.Kind == chunkclient.KindBool
	case column.Type.Kind == chunkclient.ColumnFloat32 || column.Type.Kind == chunkclient.ColumnFloat64:
		valid = v.Kind == chunkclient.KindDouble
	default:
		valid = v.Kind == chunkclient.KindBulk
	}
	if !valid {
		return fmt.Errorf("WATCH: invalid value for column %s (%s)", column.Name, column.Type)
	}
	return nil
}

func printWatchEvent(w io.Writer, push chunkclient.Value, json bool, schemas map[uint64][]chunkclient.Column, fetch func(uint64) ([]chunkclient.Column, error)) error {
	items := push.Items
	if push.Kind != chunkclient.KindPush || len(items) < 3 {
		return errors.New("WATCH: invalid push")
	}
	kind, err := watchString(items[0])
	if err != nil {
		return err
	}
	epoch, err := watchString(items[1])
	if err != nil {
		return err
	}
	revision, err := items[2].Uint64()
	if err != nil {
		return err
	}
	position, err := parseWatchPosition(fmt.Sprintf("%s:%d", epoch, revision))
	if err != nil {
		return err
	}
	fields := orderedMap{{"type", kind}, {"position", position}}
	var out bytes.Buffer
	switch kind {
	case "resync":
		if len(items) != 3 {
			return errors.New("WATCH: resync needs three fields")
		}
		fmt.Fprintf(&out, "resync %s:%d; re-read state on another connection, then resume with --after %s:%d\n", epoch, revision, epoch, revision)
	case "schema":
		if len(items) != 5 || items[4].Kind != chunkclient.KindArray {
			return errors.New("WATCH: invalid schema push")
		}
		version, err := items[3].Uint64()
		if err != nil {
			return err
		}
		columns := make([]chunkclient.Column, len(items[4].Items))
		names := map[string]bool{}
		ids := map[uint32]bool{}
		for i, value := range items[4].Items {
			column, err := chunkclient.ParseColumn(value)
			if err != nil {
				return err
			}
			if names[column.Name] || ids[column.ID] {
				return errors.New("WATCH: duplicate schema column")
			}
			names[column.Name] = true
			ids[column.ID] = true
			columns[i] = column
		}
		if len(columns) == 0 {
			return errors.New("WATCH: empty schema")
		}
		schemas[version] = columns
		fields = append(fields, jsonField{"version", version}, jsonField{"columns", jsonValue(items[4], nil)})
		fmt.Fprintf(&out, "schema %s:%d version %d\n", epoch, revision, version)
	case "change":
		if len(items) != 7 || items[6].Kind != chunkclient.KindArray {
			return errors.New("WATCH: invalid change push")
		}
		timestamp, err := items[3].Int64()
		if err != nil {
			return err
		}
		version, err := items[5].Uint64()
		if err != nil {
			return err
		}
		var user any
		userText := "(anonymous)"
		if items[4].Kind != chunkclient.KindNull {
			name, err := watchString(items[4])
			if err != nil {
				return err
			}
			user = name
			userText = name
		}
		columns, ok := schemas[version]
		if !ok {
			columns, err = fetch(version)
			if err != nil {
				return err
			}
			schemas[version] = columns
		}
		fmt.Fprintf(&out, "change revision %d time_ms %d user %s\n", revision, timestamp, userText)
		blocks := make([]any, 0, len(items[6].Items))
		for _, block := range items[6].Items {
			if block.Kind != chunkclient.KindArray || len(block.Items) != 4 {
				return errors.New("WATCH: invalid block")
			}
			x, xText, err := watchCoordinate(block.Items[0])
			if err != nil {
				return err
			}
			y, yText, err := watchCoordinate(block.Items[1])
			if err != nil {
				return err
			}
			before, beforeText, err := watchRow(block.Items[2], columns)
			if err != nil {
				return err
			}
			after, afterText, err := watchRow(block.Items[3], columns)
			if err != nil {
				return err
			}
			blocks = append(blocks, orderedMap{{"x", x}, {"y", y}, {"before", before}, {"after", after}})
			fmt.Fprintf(&out, "  block %s %s: %s -> %s\n", xText, yText, beforeText, afterText)
		}
		fields = append(fields, jsonField{"commit_time_ms", timestamp}, jsonField{"user", user}, jsonField{"schema_version", version}, jsonField{"blocks", blocks})
	default:
		return fmt.Errorf("WATCH: unknown event %q", kind)
	}
	if json {
		return writeJSON(w, fields)
	}
	_, err = w.Write(out.Bytes())
	return err
}
