package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestAreaResponseRetainsOriginalDeadline(t *testing.T) {
	schema, info, page := areaPageFixture(t)
	for _, metadata := range []bool{false, true} {
		info.withSchema = metadata
		value := page
		if metadata {
			value = watchArray(watchInt("7"), page)
		}
		var out bytes.Buffer
		err := printAreaResponse(&out, schema, info, chunkclient.Reply{Value: value, Deadline: time.Unix(1, 0)}, statementOptions{})
		var rejected *areaReplyError
		if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &rejected) || out.Len() != 0 {
			t.Fatal(err, out.String())
		}
		if err = printAreaResponse(&out, schema, info, chunkclient.Reply{Value: value, Deadline: time.Now().Add(time.Hour)}, statementOptions{}); err != nil || out.Len() == 0 {
			t.Fatal(err, out.String())
		}
	}
}

func TestAreaDeadlineExpiresDuringDecodePreparation(t *testing.T) {
	schema, info, page := areaPageFixture(t)
	start := time.Unix(10, 0)
	for _, metadata := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "schema"}[metadata], func(t *testing.T) {
			info.withSchema = metadata
			value := page
			if metadata {
				value = watchArray(watchInt("7"), page)
			}
			response := chunkclient.Reply{Value: value, Deadline: start.Add(time.Second)}
			var output bytes.Buffer
			// Initial guard, page/map/progress/row checks and area formatting use
			// twelve checks; only the thirteenth is the final preparation guard.
			const finalCheck = 13
			observed := 0
			err := printAreaResponseWithClock(&output, schema, info, response, statementOptions{}, func() time.Time {
				observed++
				if observed == finalCheck {
					return response.Deadline
				}
				return start
			})
			var rejected *areaReplyError
			if observed != finalCheck || output.Len() != 0 || !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &rejected) {
				t.Fatal(observed, finalCheck, output.String(), err)
			}
		})
	}

}

type areaAdvancingOutput struct {
	clock    *time.Time
	deadline time.Time
	err      error
	output   bytes.Buffer
}

func (w *areaAdvancingOutput) Write(data []byte) (int, error) {
	*w.clock = w.deadline
	if w.err != nil {
		return 0, w.err
	}
	return w.output.Write(data)
}

func TestAreaPublicationPreservesLocalOutputErrors(t *testing.T) {
	schema, info, page := areaPageFixture(t)
	start := time.Unix(10, 0)
	for _, localErr := range []error{nil, io.ErrClosedPipe} {
		clock := start
		response := chunkclient.Reply{Value: page, Deadline: start.Add(time.Second)}
		output := &areaAdvancingOutput{clock: &clock, deadline: response.Deadline, err: localErr}
		err := printAreaResponseWithClock(output, schema, info, response, statementOptions{}, func() time.Time { return clock })
		var rejected *areaReplyError
		if !errors.Is(err, localErr) || errors.As(err, &rejected) || errors.Is(err, context.DeadlineExceeded) || clock != response.Deadline {
			t.Fatal(err, localErr, clock)
		}
		if localErr == nil && output.output.Len() == 0 {
			t.Fatal("successful publication emitted no output")
		}
	}
}
