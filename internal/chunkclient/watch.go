package chunkclient

import (
	"fmt"
	"time"
)

// BeginWatch starts a stream on this connection. It must not be shared with
// ordinary requests. ReadWatch has no idle deadline.
func (c *Client) BeginWatch(statement string) (string, error) {
	reply, err := c.Do(statement)
	if err != nil {
		return "", err
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
func (c *Client) ReadWatch() (Value, error) { return readReply(c.reader) }

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
