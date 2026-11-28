package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestParseCatalogWatchArgs(t *testing.T) {
	for _, args := range [][]string{{"--catalog"}, {"--catalog", "--json", "--no-reconnect"}, {"--json", "--catalog"}} {
		opts, err := parseWatchArgs(args, false)
		if err != nil || !opts.catalog || opts.statement != "WATCH CATALOG" || opts.table != "" {
			t.Fatal(args, opts, err)
		}
	}
	if opts, err := parseWatchArgs([]string{"--catalog", "--json"}, false); err != nil || !opts.json {
		t.Fatal(opts, err)
	}
	if opts, err := parseWatchArgs([]string{"--catalog", "--no-reconnect"}, true); err != nil || !opts.json || !opts.noReconnect {
		t.Fatal(opts, err)
	}
	for _, args := range [][]string{{"--catalog", "world"}, {"world", "--catalog"}, {"--catalog", "--slot", "s"}, {"--catalog", "--after", watchEpoch + ":1"}, {"--catalog", "--chunks"}} {
		if _, err := parseWatchArgs(args, false); err == nil {
			t.Error("accepted", args)
		}
	}
	// The URI path table does not apply to a catalog watch.
	if !wantsCatalog([]string{"--json", "--catalog"}) || wantsCatalog([]string{"world", "--json"}) {
		t.Fatal("wantsCatalog")
	}
}

func catalogEntry(kind, action, name string, table chunkclient.Value, data chunkclient.Value) chunkclient.Value {
	return foundationMapValue(watchBulk("kind"), watchBulk(kind), watchBulk("action"), watchBulk(action), watchBulk("name"), watchBulk(name), watchBulk("table"), table, watchBulk("data"), data)
}

func TestPrintCatalogEvent(t *testing.T) {
	null := chunkclient.Value{Kind: chunkclient.KindNull}
	flag := chunkclient.Value{Kind: chunkclient.KindBool}
	column := foundationMapValue(watchBulk("id"), watchInt("1"), watchBulk("name"), watchBulk("payload"), watchBulk("type"), watchBulk("bytes(8)"),
		watchBulk("null"), flag, watchBulk("required"), flag, watchBulk("default"), watchBulk("ab"))
	data := foundationMapValue(
		watchBulk("table"), watchBulk("world"), watchBulk("version"), watchInt("1"),
		watchBulk("columns"), watchArray(column),
		watchBulk("chunk"), watchArray(watchInt("2"), watchInt("2")),
		watchBulk("large"), watchArray(watchInt("8"), watchInt("8")), watchBulk("options"), foundationMapValue(),
		watchBulk("epoch"), watchBulk(watchEpoch), watchBulk("dims"), watchInt("2"), watchBulk("chunk_columns"), watchArray(),
	)
	filters := foundationMapValue(watchBulk("area"), null, watchBulk("column_ids"), null, watchBulk("columns"), null,
		watchBulk("no_before"), flag, watchBulk("chunks"), flag)
	events := watchArray(
		catalogEntry("table", "create", "world", null, data),
		catalogEntry("slot", "create", "consumer", watchBulk("world"), foundationMapValue(watchBulk("owner"), null, watchBulk("filters"), filters)),
		catalogEntry("user", "drop", "bot", null, null),
	)
	var out bytes.Buffer
	if err := printCatalogEvent(&out, watchPush("catalog", watchInt("125"), watchBulk("admin"), events, null), false); err != nil {
		t.Fatal(err)
	}
	if want := "catalog revision 9 time_ms 125 user admin\n  create table world\n  create slot consumer on world\n  drop user bot\n"; out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	// The whole-catalog event names no user.
	out.Reset()
	if err := printCatalogEvent(&out, watchPush("catalog", watchInt("125"), null, watchArray(), null), false); err != nil || out.String() != "catalog revision 9 time_ms 125\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	out.Reset()
	if err := printCatalogEvent(&out, watchPush("catalog", watchInt("125"), null, events, null), true); err != nil {
		t.Fatal(err)
	}
	for _, part := range []string{`{"type":"catalog","position":{"epoch":"` + watchEpoch + `","revision":9},"commit_time_ms":125,"user":null,"events":[`, `{"kind":"table","action":"create","name":"world","table":null,"data":{"table":"world","version":1,`, `"default":"6162"`, `"data":null}],"txn":null}`} {
		if !strings.Contains(out.String(), part) {
			t.Fatalf("JSON %q lacks %q", out.String(), part)
		}
	}

	out.Reset()
	if err := printCatalogEvent(&out, watchPush("progress"), false); err != nil || out.String() != "progress "+watchEpoch+":9\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	out.Reset()
	if err := printCatalogEvent(&out, watchPush("resync", null, null), false); err != nil || !strings.HasPrefix(out.String(), "resync "+watchEpoch+":9; the whole catalog follows") {
		t.Fatalf("%q %v", out.String(), err)
	}
	out.Reset()
	if err := printCatalogEvent(&out, watchPush("resync", null, null), true); err != nil || out.String() != `{"type":"resync","position":{"epoch":"`+watchEpoch+`","revision":9}}`+"\n" {
		t.Fatalf("%q %v", out.String(), err)
	}
	// A future push kind is skipped; a table event does not belong to the stream.
	out.Reset()
	if err := printCatalogEvent(&out, watchPush("future"), false); err != nil || out.Len() != 0 {
		t.Fatalf("%q %v", out.String(), err)
	}
	if err := printCatalogEvent(&out, watchPush("change", watchInt("1"), null, watchInt("1"), watchArray()), false); err == nil || out.Len() != 0 {
		t.Fatalf("change accepted: %q %v", out.String(), err)
	}
	broken := watchPush("catalog", watchInt("125"), null, watchArray(catalogEntry("table", "create", "world", watchInt("1"), data)), null)
	if err := printCatalogEvent(&out, broken, false); err == nil || out.Len() != 0 {
		t.Fatalf("invalid table accepted: %q %v", out.String(), err)
	}
	for _, event := range []chunkclient.Value{
		catalogEntry("table", "create", "other", null, data),
		catalogEntry("table", "drop", "world", null, data),
		catalogEntry("slot", "alter", "consumer", watchBulk("world"), foundationMapValue(watchBulk("owner"), null)),
		catalogEntry("user", "alter", "bot", null, foundationMapValue(watchBulk("name"), watchBulk("bot"))),
	} {
		broken := watchPush("catalog", watchInt("125"), null, watchArray(event), null)
		if err := printCatalogEvent(&out, broken, true); err == nil || out.Len() != 0 {
			t.Fatalf("invalid catalog record accepted: %q %v", out.String(), err)
		}
	}
}

func TestPrintSchemaChecksAndFence(t *testing.T) {
	schema := &chunkclient.Schema{Table: "units", Version: 2, Dims: 2, ChunkWidth: 2, ChunkHeight: 2, LargeWidth: 8, LargeHeight: 8}
	schema.Columns = []chunkclient.Column{{Name: "hp", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnSigned, Size: 16}, Default: chunkclient.Value{Kind: chunkclient.KindNull}}}
	schema.Checks = []chunkclient.Check{{Name: "hp_check", Expression: "hp >= 0"}, {Name: "hp_max", Expression: "hp <= 100"}}
	schema.Fenced = "recovery failed"
	var out bytes.Buffer
	if err := printSchema(&out, schema); err != nil {
		t.Fatal(err)
	}
	if want := "columns:\n  hp i16\nchecks:\n  hp_check (hp >= 0)\n  hp_max (hp <= 100)\noptions:\nfenced = recovery failed\n"; !strings.HasSuffix(out.String(), want) {
		t.Fatalf("got %q, want suffix %q", out.String(), want)
	}
	schema.Checks, schema.Fenced = nil, ""
	out.Reset()
	if err := printSchema(&out, schema); err != nil || strings.Contains(out.String(), "checks") || strings.Contains(out.String(), "fenced") {
		t.Fatalf("%q %v", out.String(), err)
	}
}
