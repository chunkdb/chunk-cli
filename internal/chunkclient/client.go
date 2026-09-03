package chunkclient

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

type Config struct {
	URI           chunkuri.Parsed
	Timeout       time.Duration
	TLSInsecure   bool
	TLSServerName string
}

type ResponseKind int

const (
	ResponseSimple ResponseKind = iota + 1
	ResponseBulk
	ResponseArray
)

type Response struct {
	Kind   ResponseKind
	Simple string
	Bulk   []byte
	Array  [][]byte
}

type ServerError struct {
	Message string
}

func (e *ServerError) Error() string {
	return e.Message
}

type Client struct {
	conn    net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	timeout time.Duration
}

func Dial(cfg Config) (*Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}

	dialer := net.Dialer{Timeout: cfg.Timeout}
	address := cfg.URI.Address()

	var (
		conn net.Conn
		err  error
	)

	if cfg.URI.Secure {
		tlsCfg := &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: cfg.TLSInsecure,
		}
		if cfg.TLSServerName != "" {
			tlsCfg.ServerName = cfg.TLSServerName
		} else {
			tlsCfg.ServerName = cfg.URI.Host
		}

		conn, err = tls.DialWithDialer(&dialer, "tcp", address, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", address)
	}

	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", address, err)
	}

	return &Client{
		conn:    conn,
		reader:  bufio.NewReader(conn),
		writer:  bufio.NewWriter(conn),
		timeout: cfg.Timeout,
	}, nil
}

func (c *Client) Close() error {
	if c.conn == nil {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) Command(command string) (Response, error) {
	return c.CommandWithPayload(command, nil)
}

// CommandWithPayload sends a request line followed by raw payload bytes and an
// empty line (the CHUNKSETBIN framing). A nil payload sends the line alone.
func (c *Client) CommandWithPayload(command string, payload []byte) (Response, error) {
	if c.conn == nil {
		return Response{}, fmt.Errorf("connection is closed")
	}

	if strings.ContainsAny(command, "\r\n") {
		return Response{}, fmt.Errorf("command contains invalid control characters")
	}

	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return Response{}, fmt.Errorf("set deadline: %w", err)
	}

	if _, err := c.writer.WriteString(command + "\r\n"); err != nil {
		return Response{}, fmt.Errorf("write command: %w", err)
	}
	if payload != nil {
		if _, err := c.writer.Write(payload); err != nil {
			return Response{}, fmt.Errorf("write payload: %w", err)
		}
		if _, err := c.writer.WriteString("\r\n"); err != nil {
			return Response{}, fmt.Errorf("write payload terminator: %w", err)
		}
	}
	if err := c.writer.Flush(); err != nil {
		return Response{}, fmt.Errorf("flush command: %w", err)
	}

	line, err := c.reader.ReadString('\n')
	if err != nil {
		return Response{}, fmt.Errorf("read response header: %w", err)
	}

	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return Response{}, fmt.Errorf("empty response")
	}

	switch line[0] {
	case '+':
		return Response{Kind: ResponseSimple, Simple: line[1:]}, nil
	case '-':
		msg := line[1:]
		if strings.HasPrefix(msg, "ERR ") {
			msg = msg[4:]
		}
		return Response{}, &ServerError{Message: msg}
	case '$':
		payload, err := c.readBulkPayload(line)
		if err != nil {
			return Response{}, err
		}
		return Response{Kind: ResponseBulk, Bulk: payload}, nil
	case '*':
		count, err := strconv.Atoi(strings.TrimSpace(line[1:]))
		if err != nil || count < 0 {
			return Response{}, fmt.Errorf("invalid array length: %q", line)
		}
		items := make([][]byte, 0, count)
		for i := 0; i < count; i++ {
			header, err := c.reader.ReadString('\n')
			if err != nil {
				return Response{}, fmt.Errorf("read array item header: %w", err)
			}
			header = strings.TrimRight(header, "\r\n")
			if header == "" || header[0] != '$' {
				return Response{}, fmt.Errorf("invalid array item header: %q", header)
			}
			payload, err := c.readBulkPayload(header)
			if err != nil {
				return Response{}, err
			}
			items = append(items, payload)
		}
		return Response{Kind: ResponseArray, Array: items}, nil
	default:
		return Response{}, fmt.Errorf("unsupported response type: %q", line)
	}
}

// readBulkPayload reads a bulk payload + trailing CRLF given an already-read
// "$<len>" header line.
func (c *Client) readBulkPayload(header string) ([]byte, error) {
	length, err := strconv.Atoi(strings.TrimSpace(header[1:]))
	if err != nil || length < 0 {
		return nil, fmt.Errorf("invalid bulk length: %q", header)
	}

	payload := make([]byte, length)
	if _, err := io.ReadFull(c.reader, payload); err != nil {
		return nil, fmt.Errorf("read bulk payload: %w", err)
	}

	terminator := make([]byte, 2)
	if _, err := io.ReadFull(c.reader, terminator); err != nil {
		return nil, fmt.Errorf("read bulk terminator: %w", err)
	}
	if terminator[0] != '\r' || terminator[1] != '\n' {
		return nil, fmt.Errorf("invalid bulk terminator")
	}
	return payload, nil
}
