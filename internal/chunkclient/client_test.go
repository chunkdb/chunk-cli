package chunkclient

import (
    "bufio"
    "net"
    "testing"
    "time"
)

func newPipeClient(t *testing.T, handler func(server net.Conn)) *Client {
    t.Helper()

    clientConn, serverConn := net.Pipe()

    go func() {
        defer serverConn.Close()
        handler(serverConn)
    }()

    return &Client{
        conn:    clientConn,
        reader:  bufio.NewReader(clientConn),
        writer:  bufio.NewWriter(clientConn),
        timeout: 2 * time.Second,
    }
}

func TestCommandSimpleResponse(t *testing.T) {
    c := newPipeClient(t, func(server net.Conn) {
        buf := make([]byte, 64)
        _, _ = server.Read(buf)
        _, _ = server.Write([]byte("+PONG\r\n"))
    })
    defer c.Close()

    resp, err := c.Command("PING")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if resp.Kind != ResponseSimple || resp.Simple != "PONG" {
        t.Fatalf("unexpected response: %#v", resp)
    }
}

func TestCommandBulkResponse(t *testing.T) {
    c := newPipeClient(t, func(server net.Conn) {
        buf := make([]byte, 64)
        _, _ = server.Read(buf)
        _, _ = server.Write([]byte("$3\r\nabc\r\n"))
    })
    defer c.Close()

    resp, err := c.Command("GET 0 0")
    if err != nil {
        t.Fatalf("unexpected error: %v", err)
    }

    if resp.Kind != ResponseBulk || string(resp.Bulk) != "abc" {
        t.Fatalf("unexpected response: %#v", resp)
    }
}

func TestCommandServerError(t *testing.T) {
    c := newPipeClient(t, func(server net.Conn) {
        buf := make([]byte, 64)
        _, _ = server.Read(buf)
        _, _ = server.Write([]byte("-ERR AUTH_REQUIRED use AUTH <token>\r\n"))
    })
    defer c.Close()

    _, err := c.Command("INFO")
    if err == nil {
        t.Fatalf("expected server error")
    }

    if _, ok := err.(*ServerError); !ok {
        t.Fatalf("expected ServerError, got %T", err)
    }
}
