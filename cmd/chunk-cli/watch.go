package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

type watchPosition struct {
	Epoch    string `json:"epoch"`
	Revision uint64 `json:"revision"`
}

func (p watchPosition) MarshalJSON() ([]byte, error) {
	return json.Marshal(orderedMap{{"epoch", p.Epoch}, {"revision", p.Revision}})
}

type watchOptions struct {
	table     string
	statement string
	json      bool
	slot      string
	ackEvery  uint64
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
	opts := watchOptions{table: table, statement: "WATCH " + table, json: json, ackEvery: 1}
	fs := flag.NewFlagSet("watch", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var area, after string
	var columns string
	var noBefore, chunks, takeover bool
	var ackTimeout uint64
	fs.StringVar(&area, "area", "", "inclusive chunk rectangle")
	fs.StringVar(&after, "after", "", "epoch:revision")
	fs.StringVar(&opts.slot, "slot", "", "durable slot name")
	fs.StringVar(&columns, "columns", "", "comma-separated projected column names")
	fs.BoolVar(&noBefore, "no-before", false, "omit before rows")
	fs.BoolVar(&chunks, "chunks", false, "stream committed chunk forms")
	fs.BoolVar(&takeover, "takeover", false, "replace a slot's active consumer")
	fs.Uint64Var(&ackTimeout, "ack-timeout-ms", 0, "mutation acknowledgement deadline (requires --slot)")
	fs.Uint64Var(&opts.ackEvery, "ack-every", 1, "acknowledge every n printed changes (requires --slot)")
	fs.BoolVar(&opts.json, "json", json, "one JSON object per event")
	if err := fs.Parse(args[1:]); err != nil {
		return watchOptions{}, err
	}
	var emptyFlag string
	var ackEverySet, ackTimeoutSet bool
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "area" || f.Name == "after" || f.Name == "slot" || f.Name == "columns") && f.Value.String() == "" {
			emptyFlag = f.Name
		}
		if f.Name == "ack-every" {
			ackEverySet = true
		}
		if f.Name == "ack-timeout-ms" {
			ackTimeoutSet = true
		}
	})
	if emptyFlag != "" {
		return watchOptions{}, fmt.Errorf("--%s needs a value", emptyFlag)
	}
	if ackEverySet && opts.slot == "" {
		return watchOptions{}, errors.New("--ack-every requires --slot")
	}
	if (takeover || ackTimeoutSet) && opts.slot == "" {
		return watchOptions{}, errors.New("--takeover and --ack-timeout-ms require --slot")
	}
	if ackTimeoutSet && ackTimeout == 0 {
		return watchOptions{}, errors.New("--ack-timeout-ms must be positive")
	}
	if fs.NArg() != 0 {
		return watchOptions{}, fmt.Errorf("unexpected watch argument %q", fs.Arg(0))
	}
	if opts.ackEvery == 0 {
		return watchOptions{}, errors.New("--ack-every must be positive")
	}
	if opts.slot != "" {
		name := opts.slot
		if len(name) > 63 || (name[0] != '_' && (name[0] < 'a' || name[0] > 'z')) || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
			return watchOptions{}, fmt.Errorf("invalid slot %q: expected [a-z_][a-z0-9_]*, 1-63 bytes", name)
		}
		opts.statement += " SLOT '" + name + "'"
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
	if columns != "" {
		names := strings.Split(columns, ",")
		for _, name := range names {
			if name == "" || name[0] >= '0' && name[0] <= '9' || strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
				return watchOptions{}, fmt.Errorf("invalid column %q", name)
			}
		}
		opts.statement += " COLUMNS " + strings.Join(names, ", ")
	}
	if noBefore {
		opts.statement += " NO BEFORE"
	}
	if chunks {
		opts.statement += " CHUNKS"
	}
	if takeover {
		opts.statement += " TAKEOVER"
	}
	if ackTimeoutSet {
		opts.statement += fmt.Sprintf(" ACK TIMEOUT %d", ackTimeout)
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
	// A statement connection occupies a server worker until closed. Describe
	// before opening the stream so even a one-worker server can start WATCH.
	lookup := func() (*chunkclient.Schema, error) {
		describe, err := connectWatchLogin(ctx, opts, uri, login)
		if err != nil {
			return nil, err
		}
		defer describe.Close()
		var schema *chunkclient.Schema
		err = watchCall(ctx, describe, func() error { schema, err = describe.Describe(watch.table); return err })
		return schema, err
	}
	initial, err := lookup()
	if err != nil {
		return err
	}
	initialKey := watchSchemaKey{initial.Epoch, initial.Version}
	schemas := map[watchSchemaKey][]chunkclient.Column{initialKey: initial.Columns}
	metadata := map[watchSchemaKey]watchSchemaMetadata{initialKey: schemaMetadataOf(initial)}
	stream, err := connectWatchLogin(ctx, opts, uri, login)
	if err != nil {
		return err
	}
	defer stream.Close()
	var start string
	err = watchCall(ctx, stream, func() error { start, err = stream.BeginWatch(watch.statement); return err })
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
		line := fmt.Sprintf("start %s:%d\n", position.Epoch, position.Revision)
		var n int
		n, err = io.WriteString(stdout, line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
	}
	if err != nil {
		return err
	}
	return consumeWatch(ctx, watch, stream, stdout, schemas, func(watchSchemaKey) (*chunkclient.Schema, error) { return lookup() }, metadata)
}

// watchStream is the dedicated connection after WATCH has started.
type watchStream interface {
	ReadWatch() (chunkclient.Value, error)
	AckWatch(uint64) error
	EndWatch() error
	Close() error
}

func consumeWatch(ctx context.Context, watch watchOptions, stream watchStream, stdout io.Writer, schemas map[watchSchemaKey][]chunkclient.Column, fetch func(watchSchemaKey) (*chunkclient.Schema, error), contexts ...map[watchSchemaKey]watchSchemaMetadata) (resultErr error) {
	metadata := map[watchSchemaKey]watchSchemaMetadata{}
	if len(contexts) > 0 {
		metadata = contexts[0]
	}
	done := make(chan struct{})
	stopped := make(chan error, 1)
	// Acknowledgements and UNWATCH share the writer. Keep pending state under
	// the same lock so cancellation flushes only changes printed beforehand.
	var sendMu sync.Mutex
	var pending, lastRevision uint64
	ending := false
	ackPending := func() error {
		if pending == 0 {
			return nil
		}
		if err := stream.AckWatch(lastRevision); err != nil {
			return fmt.Errorf("ACK: %w", err)
		}
		pending = 0
		return nil
	}
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
			if ctx.Err() == nil {
				stopped <- nil
				return
			}
		}
		sendMu.Lock()
		ending = true
		err := ackPending()
		if err == nil {
			if err = stream.EndWatch(); err != nil {
				err = fmt.Errorf("UNWATCH: %w", err)
			}
		} else {
			_ = stream.Close()
		}
		sendMu.Unlock()
		stopped <- err
	}()
	readFailed := false
	defer func() {
		close(done)
		if stopErr := <-stopped; stopErr != nil && (resultErr == nil || readFailed) {
			resultErr = stopErr
		}
	}()
	for {
		push, err := stream.ReadWatch()
		if err != nil {
			readFailed = true
			return err
		}

		if push.Kind == chunkclient.KindSimple && push.Text == "OK" && ctx.Err() != nil {
			return nil
		}
		if push.Kind != chunkclient.KindPush {
			return fmt.Errorf("WATCH: expected a push, got %s", push.Kind)
		}
		if ctx.Err() != nil {
			continue
		} // UNWATCH drains without decoding or printing.
		if err := printWatchEvent(stdout, push, watch.json, schemas, fetch, metadata); err != nil {
			if ctx.Err() != nil {
				continue
			}
			return err
		}
		// Printing and schema lookup must not block cancellation. Register
		// only changes whose output completed before cancellation began.
		sendMu.Lock()
		if ending || ctx.Err() != nil {
			sendMu.Unlock()
			continue
		}
		kind := string(push.Items[0].Bulk)
		if watch.slot != "" && (kind == "change" || kind == "chunks" || kind == "progress") {
			lastRevision, _ = push.Items[2].Uint64() // validated by printWatchEvent
			pending++
			if pending >= watch.ackEvery {
				err = ackPending()
			}
		}
		sendMu.Unlock()
		if err != nil {
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
	if v.Kind == chunkclient.KindInteger || v.Kind == chunkclient.KindBigNumber {
		n, err := v.Int64()
		return n, v.Text, err
	}
	if v.Kind != chunkclient.KindArray || len(v.Items) < 2 {
		return nil, "", errors.New("WATCH: invalid block coordinate")
	}
	chunk, err := v.Items[0].Int64()
	if err != nil {
		return nil, "", err
	}
	offset, err := v.Items[1].Uint64()
	if err != nil || offset > math.MaxUint32 {
		return nil, "", errors.New("WATCH: invalid block offset")
	}
	return []any{chunk, offset}, fmt.Sprintf("[%d,%d]", chunk, offset), nil
}

func watchRow(row chunkclient.Value, columns []chunkclient.Column) (any, string, error) {
	if row.Kind == chunkclient.KindNull {
		return nil, "(absent)", nil
	}
	if row.Kind != chunkclient.KindArray || len(row.Items) < len(columns) {
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
	typ := column.Type
	switch {
	case v.Kind == chunkclient.KindNull:
		valid = column.Null
	case typ.Kind == chunkclient.ColumnUnsigned:
		n, err := v.Uint64()
		valid = err == nil && (typ.Size == 64 || n < uint64(1)<<typ.Size)
	case typ.Kind == chunkclient.ColumnSigned:
		n, err := v.Int64()
		valid = err == nil && (typ.Size == 64 || n >= -(int64(1)<<(typ.Size-1)) && n < int64(1)<<(typ.Size-1))
	case typ.Kind == chunkclient.ColumnBool:
		valid = v.Kind == chunkclient.KindBool
	case typ.Kind == chunkclient.ColumnFloat32 || typ.Kind == chunkclient.ColumnFloat64:
		n, err := v.Float64()
		valid = err == nil && (typ.Kind == chunkclient.ColumnFloat64 || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) <= math.MaxFloat32)
	case typ.Kind == chunkclient.ColumnBits:
		valid = v.Kind == chunkclient.KindBulk && len(v.Bulk) == (typ.Size+7)/8
		if valid && typ.Size%8 != 0 {
			valid = v.Bulk[len(v.Bulk)-1]>>uint(typ.Size%8) == 0
		}
	case typ.Kind == chunkclient.ColumnText:
		valid = v.Kind == chunkclient.KindBulk && len(v.Bulk) <= typ.Size && utf8.Valid(v.Bulk)
	case typ.Kind == chunkclient.ColumnBytes:
		valid = v.Kind == chunkclient.KindBulk && len(v.Bulk) <= typ.Size
	case typ.Kind == chunkclient.ColumnUnknown:
		valid = v.Kind == chunkclient.KindBulk
	}
	if !valid {
		return fmt.Errorf("WATCH: invalid value for column %s (%s)", column.Name, typ)
	}
	return nil
}

func printWatchEvent(w io.Writer, push chunkclient.Value, json bool, schemas map[watchSchemaKey][]chunkclient.Column, fetch func(watchSchemaKey) (*chunkclient.Schema, error), contexts ...map[watchSchemaKey]watchSchemaMetadata) error {
	if chunkclient.UnknownPush(push) {
		return nil
	}
	metadata := map[watchSchemaKey]watchSchemaMetadata{}
	if len(contexts) != 0 {
		metadata = contexts[0]
	}
	var schemaMetadata watchSchemaMetadata
	if push.Kind == chunkclient.KindPush && len(push.Items) > 0 && string(push.Items[0].Bulk) == "schema" {
		var err error
		push, schemaMetadata, err = normalizeSchemaPush(push)
		if err != nil {
			return err
		}
	}
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
	case "progress":
		fmt.Fprintf(&out, "progress %s:%d\n", epoch, revision)
	case "resync":
		if len(items) == 4 {
			return errors.New("WATCH: resync needs three fields")
		}
		var parent, fork any
		if len(items) >= 5 {
			if items[3].Kind != chunkclient.KindNull {
				text, err := watchString(items[3])
				if err != nil || !chunkclient.ValidEpoch(text) {
					return errors.New("WATCH: invalid parent epoch")
				}
				parent = text
			}
			if items[4].Kind != chunkclient.KindNull {
				n, err := items[4].Uint64()
				if err != nil {
					return err
				}
				fork = n
			}
			if (parent == nil) != (fork == nil) {
				return errors.New("WATCH: incomplete lineage")
			}
		}
		fields = append(fields, jsonField{"parent_epoch", parent}, jsonField{"fork_revision", fork})
		fmt.Fprintf(&out, "resync %s:%d; keep reading while re-reading state on another connection; retain this frontier for --after %s:%d\n", epoch, revision, epoch, revision)
	case "schema":
		if len(items) < 5 || items[4].Kind != chunkclient.KindArray {
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
		key := watchSchemaKey{position.Epoch, version}
		schemas[key] = columns
		metadata[key] = schemaMetadata
		fields = append(fields, jsonField{"version", version}, jsonField{"columns", jsonValue(items[4], nil)})
		fields = append(fields, jsonField{"dims", schemaMetadata.dims})
		fields = append(fields, jsonField{"chunk_columns", columnsJSON(schemaMetadata.chunkColumns)})
		fmt.Fprintf(&out, "schema %s:%d version %d\n", epoch, revision, version)
	case "change", "chunks":
		if len(items) < 7 || items[6].Kind != chunkclient.KindArray || kind == "change" && len(items) != 7 && len(items) < 10 || kind == "chunks" && len(items) < 9 {
			return errors.New("WATCH: invalid change push")
		}
		timestamp, err := items[3].Uint64()
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
		key := watchSchemaKey{position.Epoch, version}
		columns, ok := schemas[key]
		if !ok {
			// Empty epochs are deliberate legacy descriptions, never evidence
			// that a layout belongs to the current event's table incarnation.
			key = watchSchemaKey{version: version}
			columns, ok = schemas[key]
		}
		if !ok {
			if fetch == nil {
				return errors.New("WATCH: no schema for event")
			}
			schema, fetchErr := fetch(watchSchemaKey{position.Epoch, version})
			if fetchErr != nil {
				return fetchErr
			}
			if schema == nil {
				return errors.New("WATCH: DESCRIBE returned no schema")
			}
			if schema.Version != version {
				return fmt.Errorf("WATCH: need schema version %d, DESCRIBE returned %d", version, schema.Version)
			}
			if schema.Epoch != "" && schema.Epoch != position.Epoch {
				return fmt.Errorf("WATCH: need schema epoch %s, DESCRIBE returned %s", position.Epoch, schema.Epoch)
			}
			key = watchSchemaKey{schema.Epoch, schema.Version}
			columns = schema.Columns
			schemas[key] = columns
			metadata[key] = schemaMetadataOf(schema)
		}
		info, found := metadata[key]
		if !found {
			info = watchSchemaMetadata{dims: 2}
		}

		var tag, txn any
		if len(items) >= 9 {
			tag, err = nullableTag(items[7])
			if err != nil {
				return err
			}
			txn, err = nullableTxn(items[8])
			if err != nil {
				return err
			}
		}
		if kind == "chunks" {
			fmt.Fprintf(&out, "chunks revision %d time_ms %d user %s\n", revision, timestamp, userText)
			chunks, err := printChunkPush(&out, items[6], info.dims)
			if err != nil {
				return err
			}
			fields = append(fields, jsonField{"commit_time_ms", timestamp}, jsonField{"user", user}, jsonField{"schema_version", version}, jsonField{"chunks", chunks}, jsonField{"tag", tag}, jsonField{"txn", txn})
			break
		}
		fmt.Fprintf(&out, "change revision %d time_ms %d user %s\n", revision, timestamp, userText)
		blocks := make([]any, 0, len(items[6].Items))
		for _, block := range items[6].Items {
			if block.Kind != chunkclient.KindArray || len(block.Items) < info.dims+2 {
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
			before, beforeText, err := watchRow(block.Items[info.dims], columns)
			if err != nil {
				return err
			}
			after, afterText, err := watchRow(block.Items[info.dims+1], columns)
			if err != nil {
				return err
			}
			blockFields := orderedMap{{"x", x}, {"y", y}}
			coordinateText := xText + " " + yText
			if info.dims == 3 {
				z, zText, err := watchCoordinate(block.Items[2])
				if err != nil {
					return err
				}
				blockFields = append(blockFields, jsonField{"z", z})
				coordinateText += " " + zText
			}
			blocks = append(blocks, append(blockFields, jsonField{"before", before}, jsonField{"after", after}))
			fmt.Fprintf(&out, "  block %s: %s -> %s\n", coordinateText, beforeText, afterText)
		}
		fields = append(fields, jsonField{"commit_time_ms", timestamp}, jsonField{"user", user}, jsonField{"schema_version", version}, jsonField{"blocks", blocks})
		changes := []any{}
		if len(items) >= 10 {
			changes, err = chunkChangesJSON(&out, items[9], info)
			if err != nil {
				return err
			}
		}
		fields = append(fields, jsonField{"tag", tag}, jsonField{"txn", txn}, jsonField{"chunk_changes", changes})
	case "catalog":
		if len(items) < 7 || items[5].Kind != chunkclient.KindArray {
			return errors.New("WATCH: invalid catalog event")
		}
		timestamp, err := items[3].Uint64()
		if err != nil {
			return err
		}
		var user any
		if items[4].Kind != chunkclient.KindNull {
			user, err = watchString(items[4])
			if err != nil {
				return err
			}
		}
		txn, err := nullableTxn(items[6])
		if err != nil {
			return err
		}
		for _, event := range items[5].Items {
			if event.Kind != chunkclient.KindMap {
				return errors.New("WATCH: invalid catalog entry")
			}
			for _, key := range []string{"kind", "action", "name", "table"} {
				value, found := event.Lookup(key)
				if !found || value.Kind != chunkclient.KindBulk && value.Kind != chunkclient.KindNull {
					return fmt.Errorf("WATCH: invalid catalog %s", key)
				}
			}
			data, found := event.Lookup("data")
			if !found || data.Kind != chunkclient.KindMap && data.Kind != chunkclient.KindNull {
				return errors.New("WATCH: invalid catalog data")
			}
		}
		fields = append(fields, jsonField{"commit_time_ms", timestamp}, jsonField{"user", user}, jsonField{"events", jsonValue(items[5], nil)}, jsonField{"txn", txn})
		fmt.Fprintf(&out, "catalog %s:%d\n", epoch, revision)
	default:
		return fmt.Errorf("WATCH: unknown event %q", kind)
	}
	if json {
		return writeJSON(w, fields)
	}
	n, err := w.Write(out.Bytes())
	if err == nil && n != out.Len() {
		return io.ErrShortWrite
	}
	return err
}

// watchCall cancels a pending handshake or schema request by closing its
// connection. Stream cancellation itself uses UNWATCH once setup has finished.
func watchCall(ctx context.Context, client *chunkclient.Client, call func() error) error {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			_ = client.Close()
		case <-done:
		}
	}()
	defer func() { close(done); <-finished }()
	return call()
}

func connectWatchLogin(ctx context.Context, opts globalOptions, uri chunkuri.Parsed, login chunkclient.Login) (*chunkclient.Client, error) {
	client, err := chunkclient.DialContext(ctx, chunkclient.Config{URI: uri, Timeout: opts.Timeout, TLSInsecure: opts.TLSInsecure, TLSServerName: opts.TLSServerName, SessionOptions: opts.SessionOptions})
	if err != nil {
		return nil, err
	}
	err = watchCall(ctx, client, func() error { _, err := client.Hello(login); return err })
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connecting failed: %w", err)
	}
	return client, nil
}
