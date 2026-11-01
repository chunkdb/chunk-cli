package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

var areaReadPrefix = regexp.MustCompile(`(?i)^[ \t]*GET[ \t]+AREA\b`)

func isAreaReadStatement(statement string) bool { return areaReadPrefix.MatchString(statement) }

type areaPageRequest struct {
	dims      int
	first     [3]int64
	last      [3]int64
	around    bool
	radius    uint64
	limit     uint64
	after     *[3]int64
	maxChunks uint64
}

type areaReplyError struct{ err error }

func (e *areaReplyError) Error() string { return e.err.Error() }
func (e *areaReplyError) Unwrap() error { return e.err }

func malformedAreaReply(err error) error {
	var server *chunkclient.ServerError
	var limit *chunkclient.DecodeLimitError
	if err == nil || errors.As(err, &server) || errors.As(err, &limit) {
		return err
	}
	return &areaReplyError{err: err}
}

func areaReadLimits(server *chunkclient.ServerInfo, statement string, info statementInfo) (*chunkclient.ReplyLimits, error) {
	if !isAreaReadStatement(statement) {
		return nil, nil
	}
	if server == nil || server.MaxAreaChunks == 0 || server.MaxResponseBytes == 0 {
		return nil, fmt.Errorf("GET AREA: negotiated reply limits must be positive")
	}
	for name, advertised := range map[string]uint64{"max_area_chunks": server.MaxAreaChunks, "max_area_response_bytes": server.MaxResponseBytes} {
		if value, exists := server.Limits[name]; exists && value != advertised {
			return nil, fmt.Errorf("GET AREA: inconsistent negotiated %s", name)
		}
	}
	limit := server.MaxAreaChunks
	if info.areaPage != nil && info.areaPage.limit != 0 && info.areaPage.limit < limit {
		limit = info.areaPage.limit
	}
	return &chunkclient.ReplyLimits{MaxBytes: server.MaxResponseBytes, MaxAreaChunks: limit,
		Page: info.areaPage != nil, WithSchema: info.withSchema, UnknownShape: info.kind != statementGetArea}, nil
}

func limitedStatement(client *chunkclient.Client, statement string, limits *chunkclient.ReplyLimits, parameters [][]byte) (chunkclient.Value, error) {
	if limits == nil {
		return client.Do(statement, parameters...)
	}
	replies, err := client.Pipeline([]chunkclient.Request{{Statement: statement, Parameters: parameters, ReplyLimits: limits}})
	if err != nil {
		return chunkclient.Value{}, err
	}
	if replies[0].Err != nil {
		return chunkclient.Value{}, replies[0].Err
	}
	return replies[0].Value, nil
}

func parseAreaSelector(tokens []string) *areaPageRequest {
	request := &areaPageRequest{}
	if len(tokens) > 0 && strings.EqualFold(tokens[0], "AROUND") {
		request.around = true
		request.dims = len(tokens) - 3
		if request.dims != 2 && request.dims != 3 || !strings.EqualFold(tokens[len(tokens)-2], "RADIUS") {
			return nil
		}
		for axis := range request.dims {
			value, err := strconv.ParseInt(tokens[axis+1], 10, 64)
			if err != nil {
				return nil
			}
			request.first[axis] = value
		}
		radius, err := strconv.ParseUint(tokens[len(tokens)-1], 10, 63)
		if err != nil {
			return nil
		}
		request.radius = radius
		return request
	}
	request.dims = (len(tokens) - 1) / 2
	if request.dims != 2 && request.dims != 3 || len(tokens) != request.dims*2+1 || !strings.EqualFold(tokens[request.dims], "TO") {
		return nil
	}
	for axis := range request.dims {
		first, err := strconv.ParseInt(tokens[axis], 10, 64)
		if err != nil {
			return nil
		}
		last, err := strconv.ParseInt(tokens[request.dims+axis+1], 10, 64)
		if err != nil || first > last {
			return nil
		}
		request.first[axis], request.last[axis] = first, last
	}
	return request
}

func areaCoordLess(first, last [3]int64, dims int) bool {
	for axis := range dims {
		if first[axis] != last[axis] {
			return first[axis] < last[axis]
		}
	}
	return false
}

func (r *areaPageRequest) contains(point [3]int64) bool {
	if !r.around {
		for axis := range r.dims {
			if point[axis] < r.first[axis] || point[axis] > r.last[axis] {
				return false
			}
		}
		return true
	}
	var sum, distance, center, radius big.Int
	radius.SetUint64(r.radius)
	radius.Mul(&radius, &radius)
	for axis := range r.dims {
		distance.SetInt64(point[axis])
		center.SetInt64(r.first[axis])
		distance.Sub(&distance, &center)
		distance.Mul(&distance, &distance)
		sum.Add(&sum, &distance)
	}
	return sum.Cmp(&radius) <= 0
}

func areaClipped(value *big.Int) int64 {
	if value.IsInt64() {
		return value.Int64()
	}
	if value.Sign() < 0 {
		return math.MinInt64
	}
	return math.MaxInt64
}

func areaInterval(center int64, radius *big.Int) (int64, int64) {
	var value, distance big.Int
	value.SetInt64(center)
	distance.Sub(&value, radius)
	first := areaClipped(&distance)
	distance.Add(&value, radius)
	return first, areaClipped(&distance)
}

func (r *areaPageRequest) bounds(point [3]int64, axis int) (int64, int64) {
	if !r.around {
		return r.first[axis], r.last[axis]
	}
	var remaining, offset, center big.Int
	remaining.SetUint64(r.radius)
	remaining.Mul(&remaining, &remaining)
	for previous := 0; previous < axis; previous++ {
		offset.SetInt64(point[previous])
		center.SetInt64(r.first[previous])
		offset.Sub(&offset, &center)
		offset.Mul(&offset, &offset)
		remaining.Sub(&remaining, &offset)
	}
	remaining.Sqrt(&remaining)
	return areaInterval(r.first[axis], &remaining)
}

func (r *areaPageRequest) firstPoint() [3]int64 {
	var point [3]int64
	for axis := range r.dims {
		point[axis], _ = r.bounds(point, axis)
	}
	return point
}

func (r *areaPageRequest) nextPoint(point [3]int64) *[3]int64 {
	for axis := r.dims - 1; axis >= 0; axis-- {
		_, last := r.bounds(point, axis)
		if point[axis] < last {
			point[axis]++
			for later := axis + 1; later < r.dims; later++ {
				point[later], _ = r.bounds(point, later)
			}
			return &point
		}
	}
	return nil
}

func (r *areaPageRequest) validProgress(cursor *[3]int64, limit uint64) bool {
	valid, _ := r.validProgressChecked(cursor, limit, nil)
	return valid
}

func printAreaPage(w io.Writer, schema *chunkclient.Schema, info statementInfo, reply chunkclient.Value, opts statementOptions) error {
	if err := opts.areaCheckpoint(); err != nil {
		return err
	}
	bad := func(why string) error { return malformedAreaReply(fmt.Errorf("GET AREA: %s", why)) }
	request := info.areaPage
	dims := schema.Dims
	if dims == 0 {
		dims = 2
	}
	if request == nil || request.dims != dims || request.maxChunks == 0 {
		return bad("missing or inconsistent area page bounds")
	}
	limit := request.limit
	if limit == 0 {
		limit = request.maxChunks
	}
	if limit > request.maxChunks {
		return bad("page limit exceeds the negotiated bound")
	}
	chunks, haveChunks := reply.Lookup("chunks")
	more, haveMore := reply.Lookup("more")
	after, haveAfter := reply.Lookup("after")
	if reply.Kind != chunkclient.KindMap || !haveChunks || chunks.Kind != chunkclient.KindArray || !haveMore || more.Kind != chunkclient.KindBool || !haveAfter || uint64(len(chunks.Items)) > limit {
		return bad("expected a bounded chunks, more and after page")
	}
	seen := map[string]bool{}
	for _, field := range reply.Map {
		if err := opts.areaCheckpoint(); err != nil {
			return err
		}
		if field.Key.Kind != chunkclient.KindBulk {
			return bad("page keys must be bulk strings")
		}
		name := string(field.Key.Bulk)
		if name == "chunks" || name == "more" || name == "after" {
			if seen[name] {
				return bad("duplicate page field " + name)
			}
			seen[name] = true
		}
	}
	var cursor *[3]int64
	if more.Bool {
		if after.Kind != chunkclient.KindArray || len(after.Items) != dims {
			return bad("invalid continuation cursor arity")
		}
		cursor = &[3]int64{}
		for axis := range dims {
			value, err := after.Items[axis].Int64()
			if err != nil {
				return bad("invalid continuation coordinate")
			}
			cursor[axis] = value
		}
		if !request.contains(*cursor) || request.after != nil && !areaCoordLess(*request.after, *cursor, dims) {
			return bad("continuation cursor does not advance inside the selector")
		}
	} else if after.Kind != chunkclient.KindNull {
		return bad("a completed page must have a null cursor")
	}
	if request.after != nil && !request.contains(*request.after) {
		return bad("requested continuation is outside the selector")
	}
	valid, err := request.validProgressChecked(cursor, limit, opts.areaCheckpoint)
	if err != nil {
		return err
	}
	if !valid {
		return bad("continuation or completion exceeds the requested cell limit")
	}
	var previous *[3]int64
	for _, entry := range chunks.Items {
		if err := opts.areaCheckpoint(); err != nil {
			return err
		}
		if entry.Kind != chunkclient.KindArray || len(entry.Items) < dims+1 || entry.Items[dims].Kind != chunkclient.KindBulk {
			return bad("invalid chunk entry arity")
		}
		var point [3]int64
		for axis := range dims {
			value, err := entry.Items[axis].Int64()
			if err != nil {
				return bad("invalid chunk coordinate")
			}
			point[axis] = value
		}
		if !request.contains(point) || previous != nil && !areaCoordLess(*previous, point, dims) ||
			request.after != nil && !areaCoordLess(*request.after, point, dims) || cursor != nil && areaCoordLess(*cursor, point, dims) {
			return bad("chunk coordinates are outside the ordered page")
		}
		copy := point
		previous = &copy
	}
	var entries bytes.Buffer
	if err := printArea(&entries, schema, info.columns, chunks, opts); err != nil {
		return err
	}
	var out bytes.Buffer
	var next any
	if cursor != nil {
		coordinates := make([]any, dims)
		for axis := range dims {
			coordinates[axis] = cursor[axis]
		}
		next = coordinates
	}
	if opts.json {
		decoder := json.NewDecoder(&entries)
		decoder.UseNumber()
		var values any
		if err := decoder.Decode(&values); err != nil {
			return err
		}
		return writeJSON(w, orderedMap{{"chunks", values}, {"more", more.Bool}, {"after", next}})
	}
	fmt.Fprintf(&out, "more = %t\nafter = ", more.Bool)
	if cursor == nil {
		out.WriteString("NULL\n")
	} else {
		out.WriteByte('[')
		for axis := range dims {
			if axis != 0 {
				out.WriteString(", ")
			}
			fmt.Fprintf(&out, "%d", cursor[axis])
		}
		out.WriteString("]\n")
	}
	out.Write(entries.Bytes())
	_, err = w.Write(out.Bytes())
	return err
}
