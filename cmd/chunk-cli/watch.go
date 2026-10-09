package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

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
	fs.StringVar(&area, "area", "", "inclusive chunk rectangle")
	fs.StringVar(&after, "after", "", "epoch:revision")
	fs.StringVar(&opts.slot, "slot", "", "durable slot name")
	fs.Uint64Var(&opts.ackEvery, "ack-every", 1, "acknowledge every n printed changes (requires --slot)")
	fs.BoolVar(&opts.json, "json", json, "one JSON object per event")
	if err := fs.Parse(args[1:]); err != nil {
		return watchOptions{}, err
	}
	var emptyFlag string
	fs.Visit(func(f *flag.Flag) {
		if (f.Name == "area" || f.Name == "after" || f.Name == "slot") && f.Value.String() == "" {
			emptyFlag = f.Name
		}
		if f.Name == "ack-every" && opts.slot == "" {
			emptyFlag = "slot"
		}
	})
	if emptyFlag != "" {
		return watchOptions{}, fmt.Errorf("--%s needs a value", emptyFlag)
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
	schemas := map[uint64][]chunkclient.Column{initial.Version: initial.Columns}
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
	done := make(chan struct{})
	stopped := make(chan error, 1)
	// Acknowledgements and UNWATCH share the writer. Keep pending state under
	// the same lock so cancellation flushes only fully printed changes.
	var sendMu sync.Mutex
	var pending, lastRevision uint64
	ending := false
	ackPending := func() error {
		if pending == 0 {
			return nil
		}
		if err := stream.AckWatch(lastRevision); err != nil {
			return err
		}
		pending = 0
		return nil
	}
	go func() {
		select {
		case <-ctx.Done():
			sendMu.Lock()
			ending = true
			err := ackPending()
			if err == nil {
				err = stream.EndWatch()
			} else {
				_ = stream.Close()
			}
			sendMu.Unlock()
			stopped <- err
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
		if ctx.Err() != nil {
			continue
		} // UNWATCH drains without decoding or printing.
		// Finish printing and register its position before cancellation can
		// send the final ACK and UNWATCH.
		sendMu.Lock()
		if ending {
			sendMu.Unlock()
			continue
		}
		if err := printWatchEvent(stdout, push, watch.json, schemas, func(version uint64) ([]chunkclient.Column, error) {
			schema, err := lookup()
			if err != nil {
				return nil, err
			}
			if schema.Version != version {
				return nil, fmt.Errorf("WATCH: need schema version %d, DESCRIBE returned %d", version, schema.Version)
			}
			return schema.Columns, nil
		}); err != nil {
			sendMu.Unlock()
			if ctx.Err() != nil {
				continue
			}
			return err
		}
		if watch.slot != "" && string(push.Items[0].Bulk) == "change" {
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
	if err != nil || offset > math.MaxUint32 {
		return nil, "", errors.New("WATCH: invalid block offset")
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
	}
	if !valid {
		return fmt.Errorf("WATCH: invalid value for column %s (%s)", column.Name, typ)
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
		fmt.Fprintf(&out, "resync %s:%d; keep reading while re-reading state on another connection; retain this frontier for --after %s:%d\n", epoch, revision, epoch, revision)
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
	client, err := chunkclient.DialContext(ctx, chunkclient.Config{URI: uri, Timeout: opts.Timeout, TLSInsecure: opts.TLSInsecure, TLSServerName: opts.TLSServerName})
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
