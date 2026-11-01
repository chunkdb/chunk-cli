package chunkclient

import (
	"bufio"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

type headerOnlyReader struct {
	data string
	body bool
}

func (r *headerOnlyReader) Read(output []byte) (int, error) {
	if r.data != "" {
		count := copy(output, r.data)
		r.data = r.data[count:]
		return count, nil
	}
	r.body = true
	return 0, fmt.Errorf("body must not be read")
}

const emptyAreaPage = "%3\r\n$6\r\nchunks\r\n*0\r\n$4\r\nmore\r\n#f\r\n$5\r\nafter\r\n_\r\n"

func TestAreaReplyBudgetRejectsBeforeBodyAllocation(t *testing.T) {
	for _, tc := range []struct {
		name, header string
		limits       ReplyLimits
	}{
		{"bulk", "%3\r\n$6\r\nchunks\r\n*1\r\n*3\r\n:0\r\n:0\r\n$33554432\r\n", ReplyLimits{MaxBytes: 80, MaxAreaChunks: 2, Page: true}},
		{"line", "*123", ReplyLimits{MaxBytes: 4, MaxAreaChunks: 2}},
		{"rows", "*3\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2}},
		{"page-rows", "%1\r\n$6\r\nchunks\r\n*3\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2, Page: true}},
		{"simple-page-rows", "%1\r\n+chunks\r\n*3\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2, Page: true}},
		{"schema-rows", "*2\r\n:7\r\n*3\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2, WithSchema: true}},
		{"schema-page-rows", "*2\r\n:7\r\n%1\r\n$6\r\nchunks\r\n*3\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2, Page: true, WithSchema: true}},
		{"simple-schema-page-rows", "*2\r\n:7\r\n%1\r\n+chunks\r\n*3\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2, Page: true, WithSchema: true}},
		{"aggregate-slots", "%1\r\n$5\r\nextra\r\n*10000000\r\n", ReplyLimits{MaxBytes: 67108864, MaxAreaChunks: 2, Page: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := &headerOnlyReader{data: tc.header}
			reader := bufio.NewReader(input)
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			_, err := readReplyLimited(reader, &tc.limits)
			runtime.ReadMemStats(&after)
			var limit *replyLimitError
			if !errors.As(err, &limit) || input.body || after.TotalAlloc-before.TotalAlloc > 4*1024*1024 {
				t.Fatal(err, "read body", input.body, "allocated", after.TotalAlloc-before.TotalAlloc)
			}
		})
	}
}

func TestAreaReplyBudgetPreservesNextPipelineReply(t *testing.T) {
	for _, tc := range []struct {
		reply  string
		limits ReplyLimits
	}{
		{emptyAreaPage, ReplyLimits{MaxAreaChunks: 2, Page: true}},
		{"*2\r\n:7\r\n" + emptyAreaPage, ReplyLimits{MaxAreaChunks: 2, Page: true, WithSchema: true}},
		{"*0\r\n", ReplyLimits{MaxAreaChunks: 2}},
		{"*2\r\n:7\r\n*0\r\n", ReplyLimits{MaxAreaChunks: 2, WithSchema: true}},
	} {
		tc.limits.MaxBytes = uint64(len(tc.reply))
		unknown := ">2\r\n$6\r\nfuture\r\n_\r\n"
		sc := newScriptedClient(t, unknown+tc.reply+"+PONG\r\n")
		replies, err := sc.Pipeline([]Request{{Statement: "GET AREA 0 0 TO 1 0 FROM t", ReplyLimits: &tc.limits}, {Statement: "PING"}})
		if err != nil || len(replies) != 2 || replies[1].Value.Text != "PONG" || sc.Broken() != nil {
			t.Fatal(replies, err)
		}
	}
}

func TestAreaReplyBudgetPoisonsAndClosesConnection(t *testing.T) {
	limits := &ReplyLimits{MaxBytes: uint64(len(emptyAreaPage) - 1), MaxAreaChunks: 2, Page: true}
	sc := newScriptedClient(t, emptyAreaPage)
	if _, err := sc.Pipeline([]Request{{Statement: "GET AREA 0 0 TO 1 0 FROM t LIMIT 1", ReplyLimits: limits}}); err == nil || sc.Broken() == nil {
		t.Fatal(err)
	}
	if _, err := sc.Do("PING"); err == nil || !strings.Contains(err.Error(), "unusable") {
		t.Fatal(err)
	}
	select {
	case <-sc.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the peer did not observe the refused connection close")
	}
}

func TestAreaReplyBudgetRetainsServerErrors(t *testing.T) {
	sc := newScriptedClient(t, "-ERR OUT_OF_RANGE response cannot fit\r\n+PONG\r\n")
	limits := &ReplyLimits{MaxBytes: 1, MaxAreaChunks: 1, Page: true}
	replies, err := sc.Pipeline([]Request{{Statement: "GET AREA 0 0 TO 0 0 FROM t LIMIT 1", ReplyLimits: limits}, {Statement: "PING"}})
	if err != nil || replies[0].Err == nil || replies[0].Err.Code != "OUT_OF_RANGE" || replies[1].Value.Text != "PONG" || sc.Broken() != nil {
		t.Fatal(replies, err)
	}
}

func TestAreaReplyBudgetLeavesGenericBulkContract(t *testing.T) {
	value, err := readReply(bufio.NewReader(strings.NewReader("$5\r\nhello\r\n")))
	if err != nil || string(value.Bulk) != "hello" {
		t.Fatal(value, err)
	}
}

func TestAreaReplyBudgetRejectsZeroBeforeRequest(t *testing.T) {
	for _, limits := range []ReplyLimits{{MaxBytes: 0, MaxAreaChunks: 1}, {MaxBytes: 1, MaxAreaChunks: 0}} {
		sc := newScriptedClient(t, "+PONG\r\n")
		if _, err := sc.Pipeline([]Request{{Statement: "GET AREA 0 0 TO 0 0 FROM t", ReplyLimits: &limits}}); err == nil || sc.Broken() != nil {
			t.Fatal(err)
		}
		if _, err := sc.Do("PING"); err != nil || sc.sentText() != "PING\r\n" {
			t.Fatal(err)
		}
	}
}
