package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

const watchEpoch = "0123456789abcdef0123456789abcdef"

func TestWatchArguments(t *testing.T) {
	opts, err := parseWatchArgs([]string{"world", "--area", "-2,0,3,4", "--after", watchEpoch + ":18446744073709551615", "--json"}, false)
	if err != nil || opts.statement != "WATCH world AREA -2 0 TO 3 4 AFTER "+watchEpoch+" 18446744073709551615" || !opts.json {
		t.Fatalf("got %+v, %v", opts, err)
	}
	for _, args := range [][]string{nil, {"bad-name"}, {"world", "--area", "0,0,1"}, {"world", "--area", "1,0,0,0"}, {"world", "--area", "0,2,0,1"}, {"world", "--area", "9223372036854775808,0,0,0"}, {"world", "--after", "abc:1"}, {"world", "--after", watchEpoch + ":-1"}, {"world", "--after", watchEpoch + ":18446744073709551616"}, {"world", "--wat"}, {"world", "extra"}} {
		if _, err := parseWatchArgs(args, false); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
	if opts, err := parseWatchArgs([]string{"world"}, true); err != nil || !opts.json {
		t.Fatalf("global --json: %+v %v", opts, err)
	}
}

func watchBulk(s string) chunkclient.Value {
	return chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte(s)}
}
func watchInt(n string) chunkclient.Value {
	return chunkclient.Value{Kind: chunkclient.KindInteger, Text: n}
}
func watchArray(values ...chunkclient.Value) chunkclient.Value {
	return chunkclient.Value{Kind: chunkclient.KindArray, Items: values}
}
func watchPush(kind string, values ...chunkclient.Value) chunkclient.Value {
	return chunkclient.Value{Kind: chunkclient.KindPush, Items: append([]chunkclient.Value{watchBulk(kind), watchBulk(watchEpoch), watchInt("9")}, values...)}
}
func watchTestColumns() []chunkclient.Column {
	return []chunkclient.Column{{Name: "id", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnUnsigned, Size: 64}}, {Name: "name", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnText, Size: 16}, Null: true}, {Name: "flags", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnBits, Size: 5}}, {Name: "blob", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnBytes, Size: 8}}}
}

func TestWatchFormatting(t *testing.T) {
	columns := watchTestColumns()
	row := watchArray(watchInt("18446744073709551615"), watchBulk("it’s 'door'"), watchBulk(string([]byte{13})), watchBulk(string([]byte{0, 255})))
	event := watchPush("change", watchInt("-123"), watchBulk("admin"), watchInt("7"), watchArray(watchArray(watchInt("-2"), watchArray(watchInt("9223372036854775807"), watchInt("1")), chunkclient.Value{Kind: chunkclient.KindNull}, row)))
	schemas := map[uint64][]chunkclient.Column{}
	calls := 0
	fetch := func(version uint64) ([]chunkclient.Column, error) {
		calls++
		if version != 7 {
			t.Fatal(version)
		}
		return columns, nil
	}
	var human bytes.Buffer
	if err := printWatchEvent(&human, event, false, schemas, fetch); err != nil {
		t.Fatal(err)
	}
	want := "change revision 9 time_ms -123 user admin\n  block -2 [9223372036854775807,1]: (absent) -> {id = 18446744073709551615, name = 'it’s ''door''', flags = b'10110', blob = x'00ff'}\n"
	if human.String() != want {
		t.Fatalf("got %q, want %q", human.String(), want)
	}
	var output bytes.Buffer
	if err := printWatchEvent(&output, event, true, schemas, fetch); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("schema fetched %d times", calls)
	}
	decoder := json.NewDecoder(&output)
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		t.Fatal(err)
	}
	blocks := object["blocks"].([]any)
	after := blocks[0].(map[string]any)["after"].(map[string]any)
	if after["id"] != json.Number("18446744073709551615") || after["flags"] != "10110" || after["blob"] != "00ff" {
		t.Fatalf("values: %+v", after)
	}
	if before := blocks[0].(map[string]any)["before"]; before != nil {
		t.Fatal(before)
	}
	human.Reset()
	if err := printWatchEvent(&human, watchPush("resync"), false, schemas, fetch); err != nil || !strings.Contains(human.String(), "--after "+watchEpoch+":9") {
		t.Fatalf("resync %q %v", human.String(), err)
	}
}

func TestWatchRejectsMalformedEvents(t *testing.T) {
	bad := []chunkclient.Value{watchPush("unknown"), watchPush("resync", watchInt("1")), watchPush("schema", watchInt("1"), watchArray()), watchPush("change", watchInt("0"), watchInt("1"), watchInt("1"), watchArray())}
	for _, event := range bad {
		if err := printWatchEvent(&bytes.Buffer{}, event, false, map[uint64][]chunkclient.Column{}, func(uint64) ([]chunkclient.Column, error) { return nil, fmt.Errorf("no schema") }); err == nil {
			t.Fatalf("accepted %+v", event)
		}
	}
	_, _, err := watchRow(watchArray(watchBulk("wrong")), []chunkclient.Column{{Name: "id", Type: chunkclient.ColumnType{Kind: chunkclient.ColumnUnsigned, Size: 8}}})
	if err == nil {
		t.Fatal("accepted wrong value type")
	}
}

func TestWatchSchemaVersions(t *testing.T) {
	columnMap := func(typ string) chunkclient.Value {
		return chunkclient.Value{Kind: chunkclient.KindMap, Map: []chunkclient.MapEntry{{Key: watchBulk("id"), Value: watchInt("1")}, {Key: watchBulk("name"), Value: watchBulk("value")}, {Key: watchBulk("type"), Value: watchBulk(typ)}, {Key: watchBulk("null"), Value: chunkclient.Value{Kind: chunkclient.KindBool}}, {Key: watchBulk("required"), Value: chunkclient.Value{Kind: chunkclient.KindBool}}, {Key: watchBulk("default"), Value: chunkclient.Value{Kind: chunkclient.KindNull}}}}
	}
	schemas := map[uint64][]chunkclient.Column{}
	fetch := func(uint64) ([]chunkclient.Column, error) { t.Fatal("schema push was ignored"); return nil, nil }
	for version, typ := range []string{"text(16)", "bits(5)"} {
		var out bytes.Buffer
		if err := printWatchEvent(&out, watchPush("schema", watchInt(fmt.Sprint(version+1)), watchArray(columnMap(typ))), false, schemas, fetch); err != nil || !strings.Contains(out.String(), "version ") {
			t.Fatalf("schema %q %v", out.String(), err)
		}
	}
	for version, value := range []string{"door", string([]byte{13})} {
		event := watchPush("change", watchInt("1"), chunkclient.Value{Kind: chunkclient.KindNull}, watchInt(fmt.Sprint(version+1)), watchArray(watchArray(watchInt("0"), watchInt("0"), chunkclient.Value{Kind: chunkclient.KindNull}, watchArray(watchBulk(value)))))
		var out bytes.Buffer
		if err := printWatchEvent(&out, event, false, schemas, fetch); err != nil {
			t.Fatal(err)
		}
		want := []string{"value = 'door'", "value = b'10110'"}[version]
		if !strings.Contains(out.String(), want) {
			t.Fatal(out.String())
		}
	}
}

func TestWatchRejectsOutOfRangeValues(t *testing.T) {
	cases := []struct {
		typ   string
		value chunkclient.Value
	}{
		{"u8", watchInt("256")}, {"i8", watchInt("128")}, {"i8", watchInt("-129")},
		{"bits(5)", watchBulk("")}, {"bits(5)", watchBulk("xx")}, {"bits(5)", watchBulk(string([]byte{128}))},
		{"text(2)", watchBulk("abc")}, {"text(2)", watchBulk(string([]byte{255}))}, {"bytes(2)", watchBulk("abc")},
		{"f32", chunkclient.Value{Kind: chunkclient.KindDouble, Text: "1e40"}},
	}
	for _, tc := range cases {
		typ, err := chunkclient.ParseColumnType(tc.typ)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateWatchValue(tc.value, chunkclient.Column{Name: "v", Type: typ}); err == nil {
			t.Errorf("accepted %s %+v", tc.typ, tc.value)
		}
	}
	if _, _, err := watchCoordinate(watchArray(watchInt("0"), watchInt("4294967296"))); err == nil {
		t.Fatal("accepted oversized offset")
	}
	for _, args := range [][]string{{"world", "--area", ""}, {"world", "--after", ""}} {
		if _, err := parseWatchArgs(args, false); err == nil {
			t.Errorf("accepted empty flag %q", args)
		}
	}
}
