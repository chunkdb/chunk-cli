package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func (opts statementOptions) areaCheckpoint() error {
	if opts.areaCheck != nil {
		return checkAreaDeadline(opts.areaCheck.deadline, opts.areaCheck.now())
	}
	return nil
}

type areaResponseDeadline struct {
	deadline time.Time
	now      func() time.Time
}

func checkAreaDeadline(deadline, now time.Time) error {
	if !deadline.IsZero() && !now.Before(deadline) {
		return &areaReplyError{err: fmt.Errorf("GET AREA: command timeout: %w", context.DeadlineExceeded)}
	}
	return nil
}

func withAreaDeadline(deadline time.Time, now func() time.Time, decode func() error) (err error) {
	defer func() {
		if expired := checkAreaDeadline(deadline, now()); expired != nil {
			err = expired
		}
	}()
	if err = checkAreaDeadline(deadline, now()); err != nil {
		return err
	}
	return decode()
}

// The socket deadline also bounds typed validation after the frame is consumed.
func printAreaResponse(w io.Writer, schema *chunkclient.Schema, info statementInfo, response chunkclient.Reply, opts statementOptions) error {
	return printAreaResponseWithClock(w, schema, info, response, opts, time.Now)
}

func printAreaResponseWithClock(w io.Writer, schema *chunkclient.Schema, info statementInfo, response chunkclient.Reply, opts statementOptions, now func() time.Time) error {
	opts.areaCheck = &areaResponseDeadline{deadline: response.Deadline, now: now}
	var prepared bytes.Buffer
	err := withAreaDeadline(response.Deadline, now, func() error {
		if info.withVersion || info.withSchema {
			return printReadMetadata(&prepared, schema, info, response.Value, opts)
		}
		if info.areaPage != nil {
			return printAreaPage(&prepared, schema, info, response.Value, opts)
		}
		return printArea(&prepared, schema, info.columns, response.Value, opts)
	})
	if err != nil {
		return err
	}
	_, err = w.Write(prepared.Bytes())
	return err
}
