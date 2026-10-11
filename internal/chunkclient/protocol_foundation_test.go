package chunkclient

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"
)

func foundationValue(t *testing.T, wire string) Value {
	t.Helper()
	value, err := readReply(bufio.NewReader(strings.NewReader(wire)))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func foundationMap(pairs ...Value) Value {
	result := Value{Kind: KindMap}
	for i := 0; i < len(pairs); i += 2 {
		result.Map = append(result.Map, MapEntry{pairs[i], pairs[i+1]})
	}
	return result
}
func foundationBulk(text string) Value { return Value{Kind: KindBulk, Bulk: []byte(text)} }

func TestFoundationHelloReplyExtensions(t *testing.T) {
	value := foundationValue(t, helloReply)
	limits := Value{Kind: KindMap}
	for _, key := range []string{"max_area_chunks", "max_area_response_bytes", "txn_max_written_chunks", "txn_max_read_chunks", "max_watches", "max_returning_rows", "max_expression_nodes"} {
		limits.Map = append(limits.Map, MapEntry{foundationBulk(key), Value{Kind: KindBigNumber, Text: "18446744073709551615"}})
	}
	limits.Map = append(limits.Map, MapEntry{foundationBulk("future"), Value{Kind: KindArray}})
	value.Map = append(value.Map,
		MapEntry{foundationBulk("features"), Value{Kind: KindArray, Items: []Value{foundationBulk("protocol_foundation"), foundationBulk("future")}}},
		MapEntry{foundationBulk("connection_id"), Value{Kind: KindBigNumber, Text: "18446744073709551615"}},
		MapEntry{foundationBulk("idle_timeout_ms"), Value{Kind: KindInteger, Text: "0"}},
		MapEntry{foundationBulk("options"), foundationMap(foundationBulk("client_name"), foundationBulk("cli"), foundationBulk("future"), Value{Kind: KindMap})},
		MapEntry{foundationBulk("not_applied"), foundationMap(foundationBulk("statement_timeout_ms"), foundationMap(foundationBulk("reason"), foundationBulk("implementation_pending"), foundationBulk("future"), Value{Kind: KindArray}))},
		MapEntry{foundationBulk("limits"), limits}, MapEntry{foundationBulk("future"), Value{Kind: KindArray}})
	info, err := parseServerInfo(value)
	if err != nil || info.ConnectionID != math.MaxUint64 || !info.Supports("future") || info.Options["future"].Kind != KindMap || info.NotApplied["statement_timeout_ms"] != "implementation_pending" || len(info.Limits) != 7 {
		t.Fatal(info, err)
	}
	for _, key := range []string{"connection_id", "idle_timeout_ms", "options", "not_applied", "limits"} {
		copyValue := value
		copyValue.Map = append([]MapEntry{}, value.Map...)
		for i, pair := range copyValue.Map {
			if string(pair.Key.Bulk) == key {
				copyValue.Map = append(copyValue.Map[:i], copyValue.Map[i+1:]...)
				break
			}
		}
		if _, err := parseServerInfo(copyValue); err == nil {
			t.Fatal("accepted missing", key)
		}
	}
	for _, bad := range []Value{{Kind: KindInteger, Text: "-1"}, {Kind: KindBigNumber, Text: "18446744073709551616"}, {Kind: KindBulk, Bulk: []byte("1")}} {
		copyValue := value
		copyValue.Map = append([]MapEntry{}, value.Map...)
		for i, pair := range copyValue.Map {
			if string(pair.Key.Bulk) == "connection_id" {
				copyValue.Map[i].Value = bad
			}
		}
		if _, err := parseServerInfo(copyValue); err == nil {
			t.Fatal("accepted invalid connection_id", bad)
		}
	}
	wire := "%14\r\n" + helloFields + "_\r\n" + "$8\r\nfeatures\r\n*1\r\n$19\r\nprotocol_foundation\r\n$13\r\nconnection_id\r\n:1\r\n$15\r\nidle_timeout_ms\r\n:0\r\n$7\r\noptions\r\n%0\r\n$11\r\nnot_applied\r\n%0\r\n$6\r\nlimits\r\n%7\r\n"
	for _, pair := range limits.Map[:7] {
		wire += fmt.Sprintf("$%d\r\n%s\r\n(18446744073709551615\r\n", len(pair.Key.Bulk), pair.Key.Bulk)
	}
	c := newScriptedClient(t, wire)
	c.sessionOptions = map[string]Value{"client_name": foundationBulk("cli"), "future": foundationBulk("x")}
	if _, err := c.Hello(Login{}); err != nil {
		t.Fatal(err)
	}
	if sent := c.sentText(); sent != "HELLO 3 WITH client_name = $1, future = $2\r\n$3\r\ncli\r\n$1\r\nx\r\n" {
		t.Fatal(sent)
	}
}

func TestFoundationFeatureStatusAndSlotFilters(t *testing.T) {
	u64 := Value{Kind: KindBigNumber, Text: "18446744073709551615"}
	flags := foundationMap(foundationBulk("enabled"), Value{Kind: KindArray, Items: []Value{foundationBulk("future_feature")}}, foundationBulk("compat"), u64, foundationBulk("ro_compat"), u64, foundationBulk("incompat"), u64, foundationBulk("future"), Value{Kind: KindArray})
	table := flags
	table.Map = append(append([]MapEntry{}, flags.Map...), MapEntry{foundationBulk("name"), foundationBulk("world")})
	status := foundationMap(foundationBulk("directory"), flags, foundationBulk("tables"), Value{Kind: KindArray, Items: []Value{table}}, foundationBulk("activation_pending"), Value{Kind: KindBool}, foundationBulk("future"), Value{Kind: KindNull})
	if err := ValidateStatus(status); err != nil {
		t.Fatal(err)
	}
	filters := foundationMap(foundationBulk("area"), Value{Kind: KindArray, Items: []Value{{Kind: KindInteger, Text: "-2"}, {Kind: KindInteger, Text: "0"}, {Kind: KindInteger, Text: "1"}, {Kind: KindInteger, Text: "3"}, {Kind: KindInteger, Text: "4"}, {Kind: KindInteger, Text: "5"}}}, foundationBulk("columns"), Value{Kind: KindArray, Items: []Value{foundationBulk("renamed"), {Kind: KindNull}}}, foundationBulk("column_ids"), Value{Kind: KindArray, Items: []Value{{Kind: KindInteger, Text: "1"}, {Kind: KindInteger, Text: "2"}}}, foundationBulk("no_before"), Value{Kind: KindBool, Bool: true}, foundationBulk("chunks"), Value{Kind: KindBool, Bool: true})
	slot := foundationMap(foundationBulk("table"), foundationBulk("world"), foundationBulk("name"), foundationBulk("consumer"), foundationBulk("epoch"), foundationBulk("0123456789abcdef0123456789abcdef"), foundationBulk("acked"), u64, foundationBulk("retained_bytes"), u64, foundationBulk("lost"), Value{Kind: KindBool}, foundationBulk("lost_reason"), Value{Kind: KindNull}, foundationBulk("filters"), filters)
	if err := ValidateSlots(Value{Kind: KindArray, Items: []Value{slot}}); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]Value{"area": {Kind: KindArray, Items: []Value{{Kind: KindInteger, Text: "0"}}}, "columns": {Kind: KindArray}, "column_ids": {Kind: KindArray, Items: []Value{u64, u64}}, "chunks": {Kind: KindInteger, Text: "1"}} {
		broken := filters
		broken.Map = append([]MapEntry{}, filters.Map...)
		for i, pair := range broken.Map {
			if string(pair.Key.Bulk) == key {
				broken.Map[i].Value = bad
			}
		}
		copySlot := slot
		copySlot.Map = append([]MapEntry{}, slot.Map...)
		copySlot.Map[len(copySlot.Map)-1].Value = broken
		if err := ValidateSlots(Value{Kind: KindArray, Items: []Value{copySlot}}); err == nil {
			t.Fatal("accepted malformed filter", key)
		}
	}
	badFlags := flags
	badFlags.Map = append([]MapEntry{}, flags.Map...)
	badFlags.Map[1].Value = Value{Kind: KindBigNumber, Text: "18446744073709551616"}
	status.Map[0].Value = badFlags
	if err := ValidateStatus(status); err == nil {
		t.Fatal("accepted mask outside u64")
	}
}

func TestFoundationExactBigNumbers(t *testing.T) {
	for _, text := range []string{"-170141183460469231731687303715884105728", "-9223372036854775809", "-9223372036854775808", "0", "9223372036854775807", "9223372036854775808", "18446744073709551615", "18446744073709551616", "170141183460469231731687303715884105727"} {
		value := foundationValue(t, "("+text+"\r\n")
		exact, _ := new(big.Int).SetString(text, 10)
		got, err := value.BigInt()
		if err != nil || got.Cmp(exact) != 0 {
			t.Fatal(text, got, err)
		}
		i, ierr := value.Int64()
		u, uerr := value.Uint64()
		if (ierr == nil) != exact.IsInt64() || ierr == nil && i != exact.Int64() || (uerr == nil) != exact.IsUint64() || uerr == nil && u != exact.Uint64() {
			t.Fatal(text, i, ierr, u, uerr)
		}
		got.SetInt64(7)
		again, _ := value.BigInt()
		if again.Cmp(exact) != 0 {
			t.Fatal("BigInt accessor changed reply")
		}
	}
	for _, text := range []string{"", "+1", " 1", "1x", "--1", "0x10", "1.0"} {
		if _, err := readReply(bufio.NewReader(strings.NewReader("(" + text + "\r\n"))); err == nil {
			t.Fatal("accepted malformed big number", text)
		}
	}
}

func TestFoundationLexicalFraming(t *testing.T) {
	for statement, want := range map[string]int{
		"bad $1 $1": 2, "bad $3": 1, "bad $0 $999999999999999999999999999999": 2,
		`bad '$9' "$8" $2 $1`: 2, `bad 'a''$8' x'$7' b'$6' $1`: 1,
		`bad "a""$8" $1`: 1, `bad '$9 $1`: 0, `bad "unterminated $1`: 0,
		"bad! $1 nonsense $2": 2, "bad $ $x": 0,
	} {
		if got := ParameterCount(statement); got != want {
			t.Fatal(statement, got, want)
		}
	}
	client := newScriptedClient(t, "+FIRST\r\n>2\r\n$6\r\nfuture\r\n%1\r\n$6\r\nnested\r\n*1\r\n(18446744073709551616\r\n-ERR SYNTAX reason=syntax bad statement\r\n+LAST\r\n")
	replies, err := client.Pipeline([]Request{{Statement: "FIRST"}, {Statement: "bad $1 $1", Parameters: [][]byte{[]byte("one"), []byte("two")}}, {Statement: "LAST"}})
	if err != nil || len(replies) != 3 || replies[0].Value.Text != "FIRST" || replies[1].Err == nil || replies[1].Err.Code != "SYNTAX" || replies[2].Value.Text != "LAST" {
		t.Fatal(replies, err)
	}
	if got := client.sentText(); got != "FIRST\r\nbad $1 $1\r\n$3\r\none\r\n$3\r\ntwo\r\nLAST\r\n" {
		t.Fatal(got)
	}
}

func TestFoundationErrorCodesAndMetadata(t *testing.T) {
	_, err := readReply(bufio.NewReader(strings.NewReader("-ERR VERSION_MISMATCH current=18446744073709551615 extra=a%20b%3Dc malformed=%Q0 reread first\r\n")))
	var server *ServerError
	if !errors.As(err, &server) || server.Message != "reread first" || server.Keys["extra"] != "a b=c" || server.Keys["malformed"] != "%Q0" {
		t.Fatal(err, server)
	}
	if version, valid := server.CurrentVersion(); !valid || version != math.MaxUint64 {
		t.Fatal(version, valid)
	}
	for code, exit := range map[string]int{"VERSION_MISMATCH": 2, "CONDITION_FAILED": 2, "SCHEMA_MISMATCH": 2, "CONFLICT": 3, "BUSY": 4, "DISK_FULL": 4, "RATE_LIMITED": 4, "OUTCOME_UNKNOWN": 5, "MIGRATION_MISMATCH": 1, "SLOT_BUSY": 1, "CANCELLED": 1, "TIMEOUT": 1, "KILLED": 1, "CHECK_FAILED": 1, "FUTURE_CODE": 1} {
		e := &ServerError{Code: code, Message: "CONFLICT backoff unknown_outcome"}
		if e.ExitCode() != exit {
			t.Fatal(code, e.RetryClass(), e.ExitCode())
		}
	}
}

func TestFoundationHelloOptionFraming(t *testing.T) {
	options, err := normalizeSessionOptions(map[string]Value{"CLIENT_NAME": {Kind: KindBulk, Bulk: []byte("worker")}, "default_tag": {Kind: KindBulk, Bulk: []byte{0, 255}}, "statement_timeout_ms": {Kind: KindInteger, Text: "0"}, "future": {Kind: KindBool, Bool: true}})
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{sessionOptions: options}
	r := c.helloRequest("HELLO 3 USER bot $1", [][]byte{[]byte("scram")})
	if r.Statement != "HELLO 3 USER bot $1 WITH client_name = $2, default_tag = $3, future = true, statement_timeout_ms = 0" || len(r.Parameters) != 3 || string(r.Parameters[0]) != "scram" || string(r.Parameters[1]) != "worker" || string(r.Parameters[2]) != "\x00\xff" || ParameterCount(r.Statement) != 3 {
		t.Fatal(r)
	}
	for _, bad := range []map[string]Value{{"CLIENT_NAME": {Kind: KindBulk}, "client_name": {Kind: KindBulk}}, {"statement_timeout_ms": {Kind: KindInteger, Text: "-1"}}, {"default_tag": {Kind: KindBulk, Bulk: make([]byte, 257)}}} {
		if _, err := normalizeSessionOptions(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}

func TestFoundationDescribeNullableFrontierAndUnknownType(t *testing.T) {
	value := foundationValue(t, describeW)
	value.Map = append(value.Map, MapEntry{Value{Kind: KindBulk, Bulk: []byte("epoch")}, Value{Kind: KindBulk, Bulk: []byte("0123456789abcdef0123456789abcdef")}}, MapEntry{Value{Kind: KindBulk, Bulk: []byte("revision")}, Value{Kind: KindNull}}, MapEntry{Value{Kind: KindBulk, Bulk: []byte("future")}, Value{Kind: KindArray}})
	for _, revision := range []Value{{Kind: KindNull}, {Kind: KindBigNumber, Text: "18446744073709551615"}} {
		value.Map[len(value.Map)-2].Value = revision
		schema, err := ParseDescribe(value)
		if err != nil || schema.Epoch != "0123456789abcdef0123456789abcdef" || (schema.Revision == nil) != (revision.Kind == KindNull) || schema.Revision != nil && *schema.Revision != math.MaxUint64 {
			t.Fatal(schema, err)
		}
	}
	columnValue := foundationValue(t, "%6\r\n$2\r\nid\r\n:6\r\n$4\r\nname\r\n$6\r\nfuture\r\n$4\r\ntype\r\n$11\r\ndecimal(20)\r\n$4\r\nnull\r\n#f\r\n$8\r\nrequired\r\n#f\r\n$7\r\ndefault\r\n$4\r\n1.25\r\n")
	column, err := ParseColumn(columnValue)
	if err != nil || column.Type.Kind != ColumnUnknown || column.Type.String() != "decimal(20)" {
		t.Fatal(column, err)
	}
}
