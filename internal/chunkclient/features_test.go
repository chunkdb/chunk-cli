package chunkclient

import (
	"bufio"
	"strings"
	"testing"
)

func TestValidateStatusServerFields(t *testing.T) {
	u64 := Value{Kind: KindInteger, Text: "0"}
	flags := foundationMap(foundationBulk("enabled"), Value{Kind: KindArray}, foundationBulk("compat"), u64, foundationBulk("ro_compat"), u64, foundationBulk("incompat"), u64)
	table := foundationMap(append([]Value{foundationBulk("name"), foundationBulk("world"), foundationBulk("state"), foundationBulk("ok"), foundationBulk("reason"), {Kind: KindNull}}, mapPairs(flags)...)...)
	fenced := foundationMap(foundationBulk("name"), foundationBulk("broken"), foundationBulk("reason"), foundationBulk("recovery failed"))
	status := func(overrides map[string]Value) Value {
		pairs := []Value{
			foundationBulk("version"), foundationBulk("2.0.0"), foundationBulk("uptime_ms"), Value{Kind: KindInteger, Text: "15"},
			foundationBulk("read_only"), Value{Kind: KindBool}, foundationBulk("directory"), flags,
			foundationBulk("tables"), Value{Kind: KindArray, Items: []Value{table}}, foundationBulk("fenced"), Value{Kind: KindArray, Items: []Value{fenced}},
			foundationBulk("activation_pending"), Value{Kind: KindBool},
		}
		for i := 0; i < len(pairs); i += 2 {
			if value, found := overrides[string(pairs[i].Bulk)]; found {
				pairs[i+1] = value
			}
		}
		return foundationMap(pairs...)
	}
	if err := ValidateStatus(status(nil)); err != nil {
		t.Fatal(err)
	}
	badTable := table
	badTable.Map = append([]MapEntry{}, table.Map...)
	badTable.Map[1].Value = Value{Kind: KindNull}
	for key, bad := range map[string]Value{
		"version":   {Kind: KindInteger, Text: "2"},
		"uptime_ms": {Kind: KindInteger, Text: "-1"},
		"read_only": foundationBulk("no"),
		"fenced":    {Kind: KindArray, Items: []Value{foundationMap(foundationBulk("name"), foundationBulk("broken"))}},
		"tables":    {Kind: KindArray, Items: []Value{badTable}},
	} {
		if err := ValidateStatus(status(map[string]Value{key: bad})); err == nil {
			t.Error("accepted invalid", key)
		}
	}
}

// mapPairs returns a map's keys and values in order.
func mapPairs(m Value) []Value {
	var pairs []Value
	for _, entry := range m.Map {
		pairs = append(pairs, entry.Key, entry.Value)
	}
	return pairs
}

func TestValidateTableStats(t *testing.T) {
	count := Value{Kind: KindInteger, Text: "3"}
	stats := func(key string, value Value) Value {
		pairs := []Value{foundationBulk("epoch"), foundationBulk("0123456789abcdef0123456789abcdef"), foundationBulk("revision"), {Kind: KindNull}, foundationBulk("dims"), {Kind: KindInteger, Text: "3"}}
		for _, name := range []string{"chunks", "tombstones", "retained_deletion_floor", "loaded_chunks", "files", "disk_bytes", "slots", "watches"} {
			pairs = append(pairs, foundationBulk(name), count)
		}
		pairs = append(pairs, foundationBulk("future"), foundationBulk("kept"))
		for i := 0; i < len(pairs); i += 2 {
			if string(pairs[i].Bulk) == key {
				pairs[i+1] = value
			}
		}
		return foundationMap(pairs...)
	}
	if err := ValidateTableStats(stats("", Value{})); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTableStats(stats("revision", count)); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]Value{"epoch": foundationBulk("x"), "revision": foundationBulk("1"), "dims": {Kind: KindInteger, Text: "4"}, "disk_bytes": {Kind: KindInteger, Text: "-1"}, "watches": {Kind: KindNull}} {
		if err := ValidateTableStats(stats(key, bad)); err == nil {
			t.Error("accepted invalid", key)
		}
	}
	if err := ValidateTableStats(Value{Kind: KindArray}); err == nil {
		t.Error("accepted an array")
	}
}

func TestValidateSlotsFeedFields(t *testing.T) {
	u64 := Value{Kind: KindInteger, Text: "2"}
	slot := func(key string, value Value) Value {
		pairs := []Value{foundationBulk("table"), foundationBulk("world"), foundationBulk("name"), foundationBulk("consumer"), foundationBulk("epoch"), foundationBulk("0123456789abcdef0123456789abcdef"),
			foundationBulk("acked"), u64, foundationBulk("revision"), u64, foundationBulk("lag_revisions"), {Kind: KindInteger, Text: "0"}, foundationBulk("retained_bytes"), u64,
			foundationBulk("attached"), {Kind: KindBool, Bool: true}, foundationBulk("lost"), {Kind: KindBool}, foundationBulk("lost_reason"), {Kind: KindNull}}
		for i := 0; i < len(pairs); i += 2 {
			if string(pairs[i].Bulk) == key {
				pairs[i+1] = value
			}
		}
		return Value{Kind: KindArray, Items: []Value{foundationMap(pairs...)}}
	}
	if err := ValidateSlots(slot("", Value{})); err != nil {
		t.Fatal(err)
	}
	for key, bad := range map[string]Value{"revision": {Kind: KindNull}, "lag_revisions": {Kind: KindInteger, Text: "-1"}, "attached": foundationBulk("yes")} {
		if err := ValidateSlots(slot(key, bad)); err == nil {
			t.Error("accepted invalid", key)
		}
	}
}

func TestParseDescribeChecksAndFence(t *testing.T) {
	extra := "$6\r\nchecks\r\n*1\r\n%2\r\n$4\r\nname\r\n$8\r\nhp_check\r\n$10\r\nexpression\r\n$7\r\nhp >= 0\r\n" +
		"$6\r\nfenced\r\n$6\r\nbroken\r\n"
	reply := strings.Replace(describeW, "%6\r\n$5\r\ntable", "%8\r\n$5\r\ntable", 1) + extra
	schema := mustSchema(t, reply)
	if len(schema.Checks) != 1 || schema.Checks[0] != (Check{Name: "hp_check", Expression: "hp >= 0"}) || schema.Fenced != "broken" {
		t.Fatalf("checks %+v, fenced %q", schema.Checks, schema.Fenced)
	}
	if schema := mustSchema(t, describeW); schema.Checks != nil || schema.Fenced != "" {
		t.Fatalf("checks %+v, fenced %q", schema.Checks, schema.Fenced)
	}
	for _, broken := range []string{
		strings.Replace(reply, "$7\r\nhp >= 0", ":7", 1),
		strings.Replace(describeW, "%6\r\n$5\r\ntable", "%7\r\n$5\r\ntable", 1) + "$6\r\nchecks\r\n:1\r\n",
		strings.Replace(reply, "$6\r\nbroken", "$0\r\n", 1),
	} {
		value, err := readReply(bufio.NewReader(strings.NewReader(broken)))
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if _, err := ParseDescribe(value); err == nil {
			t.Errorf("accepted %q", broken[len(broken)-40:])
		}
	}
}
