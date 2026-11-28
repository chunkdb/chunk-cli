package main

import (
	"bytes"
	"fmt"
	"io"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

type scanPage struct {
	chunks   []chunkclient.Value
	dims     int
	more     bool
	epoch    string
	frontier any
	floor    uint64
	after    *[3]int64
}

func scanPoint(values []chunkclient.Value) ([3]int64, error) {
	var point [3]int64
	for axis, value := range values {
		n, err := value.Int64()
		if err != nil {
			return point, err
		}
		point[axis] = n
	}
	return point, nil
}

func badScan(why string) error { return fmt.Errorf("SCAN CHUNKS: %s", why) }

// Metadata-only scans infer the axes from their first row or cursor if the
// statement did not supply AREA or AFTER. They do not need an extra DESCRIBE.
func validateScanPage(info statementInfo, reply chunkclient.Value, schema *chunkclient.Schema) (scanPage, error) {
	var page scanPage
	chunks, haveChunks := reply.Lookup("chunks")
	more, haveMore := reply.Lookup("more")
	epoch, haveEpoch := reply.Lookup("epoch")
	position, havePosition := reply.Lookup("position")
	floor, haveFloor := reply.Lookup("retained_deletion_floor")
	if reply.Kind != chunkclient.KindMap || !haveChunks || chunks.Kind != chunkclient.KindArray || !haveMore || more.Kind != chunkclient.KindBool || !haveEpoch || epoch.Kind != chunkclient.KindBulk || !havePosition || !haveFloor {
		return page, badScan("expected chunks, more, epoch, position and retained_deletion_floor")
	}
	page.chunks, page.more, page.epoch, page.dims = chunks.Items, more.Bool, string(epoch.Bulk), info.dims
	if !chunkclient.ValidEpoch(page.epoch) || schema != nil && schema.Epoch != "" && schema.Epoch != page.epoch {
		return page, badScan("invalid or changed table epoch")
	}
	if schema != nil && schema.Dims != 0 {
		page.dims = schema.Dims
	}
	if area := info.scanArea; area != nil {
		if page.dims != 0 && page.dims != area.dims {
			return page, badScan("area dimensions do not match the scan")
		}
		page.dims = area.dims
	}
	var err error
	page.floor, err = floor.Uint64()
	if err != nil || info.scanSince != nil && *info.scanSince < page.floor {
		return page, badScan("invalid retained deletion floor")
	}
	if position.Kind != chunkclient.KindNull {
		value, err := position.Uint64()
		if err != nil {
			return page, badScan("invalid completed frontier")
		}
		page.frontier = value
	}

	withData := info.kind == statementScanChunks
	extra := 1 // Producing version, after the coordinates.
	if withData {
		extra++
	}
	var previous [3]int64
	havePrevious := info.scanAfter != nil
	if havePrevious {
		previous = *info.scanAfter
	}
	for _, item := range page.chunks {
		dims := len(item.Items) - extra
		if item.Kind != chunkclient.KindArray || dims != 2 && dims != 3 || page.dims != 0 && dims != page.dims {
			return page, badScan("invalid chunk entry arity")
		}
		page.dims = dims
		point, err := scanPoint(item.Items[:dims])
		if err != nil {
			return page, badScan("invalid chunk coordinate")
		}
		if info.scanArea != nil && !info.scanArea.contains(point) {
			return page, badScan("chunk outside the scanned area")
		}
		if havePrevious && !areaCoordLess(previous, point, dims) {
			return page, badScan("chunk coordinates do not advance in order")
		}
		previous, havePrevious = point, true
		version, err := item.Items[dims].Uint64()
		if err != nil || version == 0 || info.scanSince != nil && version <= *info.scanSince {
			return page, badScan("invalid retained chunk version")
		}
		if withData {
			form := item.Items[dims+1]
			switch {
			case form.Kind == chunkclient.KindNull && info.scanSince == nil:
				return page, badScan("ordinary scan contains a tombstone")
			case form.Kind != chunkclient.KindNull && form.Kind != chunkclient.KindBulk:
				return page, badScan("expected chunk form or tombstone")
			}
		}
	}
	if info.scanWhere {
		// A filtered page may be empty; after is the last visited chunk.
		next, ok := reply.Lookup("after")
		if !ok || page.more != (next.Kind != chunkclient.KindNull) {
			return page, badScan("missing after or after and more disagree")
		}
		if page.more {
			dims := len(next.Items)
			if next.Kind != chunkclient.KindArray || dims != 2 && dims != 3 || page.dims != 0 && dims != page.dims {
				return page, badScan("invalid after coordinate")
			}
			point, err := scanPoint(next.Items)
			if err != nil || havePrevious && areaCoordLess(point, previous, dims) ||
				info.scanAfter != nil && !areaCoordLess(*info.scanAfter, point, dims) {
				return page, badScan("invalid or non-advancing after coordinate")
			}
			if info.scanArea != nil && !info.scanArea.contains(point) {
				return page, badScan("after coordinate outside the scanned area")
			}
			page.after, page.dims = &point, dims
		}
	} else if page.more && len(page.chunks) == 0 {
		return page, badScan("empty page reports more")
	}
	return page, nil
}

// WITH DATA renders the decoded form alongside its retained identity.
func printChunkScan(w io.Writer, schema *chunkclient.Schema, info statementInfo, reply chunkclient.Value, opts statementOptions) error {
	page, err := validateScanPage(info, reply, schema)
	if err != nil {
		return err
	}
	dims := page.dims
	list := make([]any, 0, len(page.chunks))
	var out bytes.Buffer
	fmt.Fprintf(&out, "epoch = %s\nposition = ", page.epoch)
	if page.frontier == nil {
		out.WriteString("NULL\n")
	} else {
		fmt.Fprintf(&out, "%d\n", page.frontier)
	}
	fmt.Fprintf(&out, "retained_deletion_floor = %d\nmore = %t\n", page.floor, page.more)
	for _, item := range page.chunks {
		point, _ := scanPoint(item.Items[:dims])
		version, _ := item.Items[dims].Uint64()
		form := item.Items[dims+1]
		deleted := form.Kind == chunkclient.KindNull
		fields := orderedMap{}
		out.WriteString("chunk")
		for axis, value := range point[:dims] {
			fields = append(fields, jsonField{[]string{"cx", "cy", "cz"}[axis], value})
			fmt.Fprintf(&out, " %d", value)
		}
		out.WriteByte('\n')
		fields = append(fields, jsonField{"version", version}, jsonField{"deleted", deleted})
		if deleted {
			fields = append(fields, jsonField{"chunk", nil})
			fmt.Fprintf(&out, "  version = %d\n  deleted = true\n", version)
		} else {
			chunk, err := decodeOutputChunk(schema, info.columns, form.Bulk, opts.blocks)
			if err != nil {
				return err
			}
			if chunk.Version != version || chunk.Private {
				return badScan("chunk form and retained version differ")
			}
			fields = append(fields, jsonField{"chunk", chunkJSON(chunk, point[0], point[1], opts.blocks, point[2:dims]...)})
			writeChunk(&out, chunk, point[0], point[1], opts.blocks, "  ", point[2:dims]...)
		}
		list = append(list, fields)
	}
	result := orderedMap{{"chunks", list}, {"more", page.more}}
	if info.scanWhere {
		var after any
		if page.after == nil {
			out.WriteString("after = NULL\n")
		} else {
			coordinates := make([]any, dims)
			out.WriteString("after =")
			for axis, value := range page.after[:dims] {
				coordinates[axis] = value
				fmt.Fprintf(&out, " %d", value)
			}
			out.WriteByte('\n')
			after = coordinates
		}
		result = append(result, jsonField{"after", after})
	}
	if opts.json {
		return writeJSON(w, append(result, orderedMap{{"epoch", page.epoch}, {"position", page.frontier}, {"retained_deletion_floor", page.floor}}...))
	}
	_, err = w.Write(out.Bytes())
	return err
}
