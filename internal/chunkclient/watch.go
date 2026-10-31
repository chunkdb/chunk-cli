package chunkclient

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// BeginWatch starts a stream on this connection. It must not be shared with
// ordinary requests. ReadWatch has no idle deadline.
func (c *Client) BeginWatch(statement string) (string, error) {
	reply, err := c.Do(statement)
	if err != nil {
		return "", err
	}
	if reply.Kind == KindMap {
		epoch, present := reply.Lookup("epoch")
		revision, found := reply.Lookup("revision")
		number, err := revision.Uint64()
		if !present || epoch.Kind != KindBulk || !ValidEpoch(string(epoch.Bulk)) || !found || err != nil {
			return "", fmt.Errorf("WATCH: invalid start position map")
		}
		reply = Value{Kind: KindSimple, Text: "OK " + string(epoch.Bulk) + " " + strconv.FormatUint(number, 10)}
	}
	if reply.Kind != KindSimple {
		return "", fmt.Errorf("WATCH: expected a start position, got %s", reply.Kind)
	}
	if err := c.conn.SetDeadline(time.Time{}); err != nil {
		return "", err
	}
	return reply.Text, nil
}

// ReadWatch reads the next push or UNWATCH acknowledgement. One caller owns
// reads; EndWatch may run concurrently to interrupt an idle watch.
func (c *Client) ReadWatch() (Value, error) {
	for {
		value, err := readReply(c.reader)
		if err != nil || !UnknownPush(value) {
			return value, err
		}
	}
}

func ValidEpoch(epoch string) bool {
	return len(epoch) == 32 && strings.Trim(epoch, "0123456789abcdef") == ""
}

// UnknownPush recognizes fully decoded future kinds without assuming their envelope.
func UnknownPush(value Value) bool {
	if value.Kind != KindPush || len(value.Items) == 0 || value.Items[0].Kind != KindBulk {
		return false
	}
	switch string(value.Items[0].Bulk) {
	case "schema", "change", "chunks", "progress", "resync", "catalog":
		return false
	default:
		return true
	}
}

// AckWatch acknowledges a delivered change, chunks or progress frontier. Success has no
// reply; server errors are returned by ReadWatch. Serialize with EndWatch.
func (c *Client) AckWatch(revision uint64) error {
	if err := c.conn.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	if _, err := c.writer.WriteString("ACK " + strconv.FormatUint(revision, 10) + "\r\n"); err != nil {
		return err
	}
	if err := c.writer.Flush(); err != nil {
		return err
	}
	return c.conn.SetWriteDeadline(time.Time{})
}

// EndWatch sends UNWATCH and bounds the time spent draining queued pushes and
// its acknowledgement. Call once, then continue ReadWatch until +OK.
func (c *Client) EndWatch() error {
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		_ = c.conn.Close()
		return err
	}
	if _, err := c.writer.WriteString("UNWATCH\r\n"); err != nil {
		_ = c.conn.Close()
		return err
	}
	if err := c.writer.Flush(); err != nil {
		_ = c.conn.Close()
		return err
	}
	return nil
}
