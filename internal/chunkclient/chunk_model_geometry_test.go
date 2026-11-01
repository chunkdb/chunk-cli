package chunkclient

import (
	"bufio"
	"strconv"
	"strings"
	"testing"
)

func geometryReply(t *testing.T, dims int, chunk, large []uint64) Value {
	t.Helper()
	value, err := readReply(bufio.NewReader(strings.NewReader(describeW)))
	if err != nil {
		t.Fatal(err)
	}
	set := func(key string, replacement Value) {
		for i := range value.Map {
			if string(value.Map[i].Key.Bulk) == key {
				value.Map[i].Value = replacement
				return
			}
		}
		value.Map = append(value.Map, MapEntry{Key: Value{Kind: KindBulk, Bulk: []byte(key)}, Value: replacement})
	}
	axes := func(sides []uint64) Value {
		v := Value{Kind: KindArray}
		for _, side := range sides {
			v.Items = append(v.Items, Value{Kind: KindInteger, Text: strconv.FormatUint(side, 10)})
		}
		return v
	}
	set("dims", Value{Kind: KindInteger, Text: strconv.Itoa(dims)})
	set("chunk", axes(chunk))
	set("large", axes(large))
	return value
}

func TestChunkModelDescribeKeepsActiveDepth(t *testing.T) {
	for _, dims := range []int{2, 3} {
		chunk, large := []uint64{2, 2}, []uint64{1000000, 8}
		if dims == 3 {
			chunk = append(chunk, 2)
			large = append(large, 3)
		}
		schema, err := ParseDescribe(geometryReply(t, dims, chunk, large))
		if err != nil {
			t.Fatal(err)
		}
		blocks, depth, largeDepth := 4, 1, 1
		if dims == 3 {
			blocks, depth, largeDepth = 8, 2, 3
		}
		if schema.BlockCount() != blocks || schema.ChunkDepth != depth || schema.LargeDepth != largeDepth {
			t.Fatalf("%+v blocks=%d", schema, schema.BlockCount())
		}
	}
}

func TestChunkModelDescribeGeometryBounds(t *testing.T) {
	for _, test := range []struct {
		name         string
		dims         int
		chunk, large []uint64
	}{
		{"missing chunk depth", 3, []uint64{2, 2}, []uint64{8, 8, 8}},
		{"missing large depth", 3, []uint64{2, 2, 2}, []uint64{8, 8}},
		{"inactive chunk depth", 2, []uint64{2, 2, 1}, []uint64{8, 8}},
		{"zero depth", 3, []uint64{2, 2, 0}, []uint64{8, 8, 8}},
		{"chunk axis", 3, []uint64{2, 2, 4097}, []uint64{8, 8, 8}},
		{"chunk product", 3, []uint64{1024, 1024, 2}, []uint64{8, 8, 8}},
		{"large width", 3, []uint64{2, 2, 2}, []uint64{1000001, 8, 8}},
		{"large depth", 3, []uint64{2, 2, 2}, []uint64{8, 8, 1000001}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if schema, err := ParseDescribe(geometryReply(t, test.dims, test.chunk, test.large)); err == nil {
				t.Fatalf("accepted %+v", schema)
			}
		})
	}
	for _, schema := range []*Schema{nil, {ChunkWidth: 4097, ChunkHeight: 1}, {Dims: 3, ChunkWidth: 2, ChunkHeight: 2}, {Dims: 3, ChunkWidth: 1024, ChunkHeight: 1024, ChunkDepth: 2}} {
		if schema.BlockCount() != 0 {
			t.Fatalf("invalid count %+v", schema)
		}
	}
}
