package main

import (
	"bytes"
	"fmt"
	"io"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// WITH DATA validates retained identities independently of the producing form.
func printChunkScan(w io.Writer, schema *chunkclient.Schema, info statementInfo, reply chunkclient.Value, opts statementOptions) error {
	bad := func(why string) error { return fmt.Errorf("SCAN CHUNKS: %s", why) }
	chunks, haveChunks := reply.Lookup("chunks")
	more, haveMore := reply.Lookup("more")
	epoch, haveEpoch := reply.Lookup("epoch")
	position, havePosition := reply.Lookup("position")
	floor, haveFloor := reply.Lookup("retained_deletion_floor")
	if reply.Kind != chunkclient.KindMap || !haveChunks || chunks.Kind != chunkclient.KindArray || !haveMore || more.Kind != chunkclient.KindBool || !haveEpoch || epoch.Kind != chunkclient.KindBulk || !havePosition || !haveFloor {
		return bad("expected chunks, more, epoch, position and retained_deletion_floor")
	}
	epochText := string(epoch.Bulk)
	if len(epochText) != 32 || strings.Trim(epochText, "0123456789abcdef") != "" || schema.Epoch != "" && schema.Epoch != epochText {
		return bad("invalid or changed table epoch")
	}
	retainedFloor, err := floor.Uint64()
	if err != nil || info.scanSince != nil && *info.scanSince < retainedFloor {
		return bad("invalid retained deletion floor")
	}
	var frontier any
	if position.Kind != chunkclient.KindNull {
		value, err := position.Uint64()
		if err != nil {
			return bad("invalid completed frontier")
		}
		frontier = value
	}
	dims := schema.Dims
	if dims == 0 {
		dims = 2
	}
	list := make([]any, 0, len(chunks.Items))
	var out bytes.Buffer
	fmt.Fprintf(&out, "epoch = %s\nposition = ", epochText)
	if frontier == nil {
		out.WriteString("NULL\n")
	} else {
		fmt.Fprintf(&out, "%d\n", frontier)
	}
	fmt.Fprintf(&out, "retained_deletion_floor = %d\nmore = %t\n", retainedFloor, more.Bool)
	var previous []int64
	for _, item := range chunks.Items {
		if item.Kind != chunkclient.KindArray || len(item.Items) < dims+2 {
			return bad("invalid chunk entry arity")
		}
		point := make([]int64, dims)
		fields := orderedMap{}
		for axis := range point {
			point[axis], err = item.Items[axis].Int64()
			if err != nil {
				return bad("invalid chunk coordinate")
			}
			fields = append(fields, jsonField{[]string{"cx", "cy", "cz"}[axis], point[axis]})
		}
		if previous != nil {
			less := false
			for axis := range point {
				if previous[axis] != point[axis] {
					less = previous[axis] < point[axis]
					break
				}
			}
			if !less {
				return bad("chunk coordinates do not advance in order")
			}
		}
		previous = point
		version, err := item.Items[dims].Uint64()
		if err != nil || version == 0 || info.scanSince != nil && version <= *info.scanSince {
			return bad("invalid retained chunk version")
		}
		form := item.Items[dims+1]
		deleted := form.Kind == chunkclient.KindNull
		fields = append(fields, jsonField{"version", version}, jsonField{"deleted", deleted})
		out.WriteString("chunk")
		for _, value := range point {
			fmt.Fprintf(&out, " %d", value)
		}
		out.WriteByte('\n')
		if deleted {
			if info.scanSince == nil {
				return bad("ordinary scan contains a tombstone")
			}
			fields = append(fields, jsonField{"chunk", nil})
			fmt.Fprintf(&out, "  version = %d\n  deleted = true\n", version)
		} else {
			if form.Kind != chunkclient.KindBulk {
				return bad("expected chunk form or tombstone")
			}
			var chunk *chunkclient.Chunk
			if opts.blocks {
				chunk, err = chunkclient.DecodeSectionedChunkForm(schema, info.columns, form.Bulk)
			} else {
				chunk, err = chunkclient.DecodeSectionedChunkSummary(schema, info.columns, form.Bulk)
			}
			if err != nil {
				return err
			}
			if chunk.Version != version || chunk.Private {
				return bad("chunk form and retained version differ")
			}
			fields = append(fields, jsonField{"chunk", chunkJSON(chunk, point[0], point[1], opts.blocks, point[2:]...)})
			writeChunk(&out, chunk, point[0], point[1], opts.blocks, "  ", point[2:]...)
		}
		list = append(list, fields)
	}
	if more.Bool && len(list) == 0 {
		return bad("empty page reports more")
	}
	if opts.json {
		return writeJSON(w, orderedMap{{"chunks", list}, {"more", more.Bool}, {"epoch", epochText}, {"position", frontier}, {"retained_deletion_floor", retainedFloor}})
	}
	_, err = w.Write(out.Bytes())
	return err
}
