package chunkclient

import (
	"bufio"
	"crypto/hmac"
	"crypto/tls"
	"errors"
	"fmt"
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

// ProtocolVersion is the chunkdb protocol this client speaks.
const ProtocolVersion = 3

// ServerInfo is the HELLO reply: the protocol, the server's limits and the
// login's server signature.
type ServerInfo struct {
	Protocol         uint64
	ServerVersion    string
	MaxLineBytes     uint64
	MaxParameters    uint64
	MaxAreaChunks    uint64
	MaxResponseBytes uint64
	MaxScanLimit     uint64
	// ServerSignature is the SCRAM server-final message of a login
	// (v=<signature>), empty without a user.
	ServerSignature string
}

// Request is one statement and its parameter frames ($1 … $n). A nil
// parameter is sent as NULL.
type Request struct {
	Statement  string
	Parameters [][]byte
}

// Reply is the outcome of one pipelined request: a value, or the server's
// error for that statement.
type Reply struct {
	Value Value
	Err   *ServerError
}

type Client struct {
	conn    net.Conn
	reader  *bufio.Reader
	writer  *bufio.Writer
	timeout time.Duration
	info    *ServerInfo
	// broken is the transport or framing error that left the connection
	// unusable.
	broken error
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

// Broken returns the error that left the connection unusable, or nil.
func (c *Client) Broken() error {
	return c.broken
}

// Info returns the HELLO reply, or nil before Hello.
func (c *Client) Info() *ServerInfo {
	return c.info
}

// Login is the user a connection logs in as; an empty User logs in without
// one (a server started with --auth none).
type Login struct {
	User     string
	Password string
}

// Hello sends HELLO 3, the first line of a connection, and returns the
// server's limits. With a user it logs in with SCRAM-SHA-256: `HELLO 3 USER
// <name> $1` with the client-first message, then `AUTH $1` with the proof;
// the password never crosses the network, and the server must prove that it
// holds the user's verifier.
func (c *Client) Hello(login Login) (*ServerInfo, error) {
	line := "HELLO " + strconv.Itoa(ProtocolVersion)
	if login.User == "" {
		reply, err := c.helloStep(Request{Statement: line}, false)
		if err != nil {
			return nil, err
		}
		return c.acceptHello(reply, "")
	}

	scram, err := startLogin(login.User, login.Password)
	if err != nil {
		return nil, err
	}
	reply, err := c.helloStep(Request{Statement: line + " USER " + login.User + " $1", Parameters: [][]byte{[]byte(scram.first)}}, true)
	if err != nil {
		return nil, err
	}
	serverFirst, ok := strings.CutPrefix(reply.Text, "SCRAM ")
	if reply.Kind != KindSimple || !ok {
		return nil, fmt.Errorf("HELLO reply: expected +SCRAM <server-first message>, got a %s", reply.Kind)
	}
	clientFinal, signature, err := scram.finish(serverFirst)
	if err != nil {
		return nil, err
	}
	reply, err = c.helloStep(Request{Statement: "AUTH $1", Parameters: [][]byte{[]byte(clientFinal)}}, true)
	if err != nil {
		return nil, err
	}
	return c.acceptHello(reply, signature)
}

// helloStep sends one line of the login and returns its reply; a server
// error is returned as a *ServerError, explained when it comes from a server
// of another protocol.
func (c *Client) helloStep(request Request, withUser bool) (Value, error) {
	replies, err := c.roundTrip([]Request{request})
	if err != nil {
		return Value{}, err
	}
	serverErr := replies[0].Err
	if serverErr == nil {
		return replies[0].Value, nil
	}
	switch {
	case serverErr.Code == "PROTOCOL" && strings.Contains(serverErr.Message, "HELLO 2"):
		return Value{}, fmt.Errorf("the server speaks an older chunkdb protocol (%s); this chunk-cli needs protocol %d", serverErr.Message, ProtocolVersion)
	// A 1.x server does not know HELLO; one that requires a token answers
	// AUTH_REQUIRED, which a server with users never answers to a login.
	case serverErr.Code == "UNKNOWN_COMMAND" || (serverErr.Code == "AUTH_REQUIRED" && withUser):
		return Value{}, fmt.Errorf("the server does not speak protocol %d (chunkdb 1.x); this chunk-cli needs a server with protocol %d", ProtocolVersion, ProtocolVersion)
	}
	return Value{}, serverErr
}

// acceptHello reads the HELLO map; after a login its server_signature must
// be the one computed from the password.
func (c *Client) acceptHello(reply Value, wantSignature string) (*ServerInfo, error) {
	info, err := parseServerInfo(reply)
	if err != nil {
		return nil, fmt.Errorf("HELLO reply: %w", err)
	}
	if wantSignature != "" && !hmac.Equal([]byte(info.ServerSignature), []byte(wantSignature)) {
		err := errors.New("the server could not prove it knows the password (wrong SCRAM server signature)")
		c.broken = err
		return nil, err
	}
	c.info = info
	return info, nil
}

func parseServerInfo(reply Value) (*ServerInfo, error) {
	if reply.Kind != KindMap {
		return nil, fmt.Errorf("expected a map, got %s", reply.Kind)
	}
	info := &ServerInfo{}
	version, ok := reply.Lookup("server_version")
	if !ok || version.Kind != KindBulk {
		return nil, errors.New("no server_version")
	}
	info.ServerVersion = string(version.Bulk)
	signature, ok := reply.Lookup("server_signature")
	switch {
	case !ok:
		return nil, errors.New("no server_signature")
	case signature.Kind == KindBulk:
		info.ServerSignature = string(signature.Bulk)
	case signature.Kind != KindNull:
		return nil, fmt.Errorf("server_signature: expected a bulk string or null, got %s", signature.Kind)
	}
	for _, field := range []struct {
		key string
		dst *uint64
	}{
		{"protocol", &info.Protocol},
		{"max_line_bytes", &info.MaxLineBytes},
		{"max_parameters", &info.MaxParameters},
		{"max_area_chunks", &info.MaxAreaChunks},
		{"max_response_bytes", &info.MaxResponseBytes},
		{"max_scan_limit", &info.MaxScanLimit},
	} {
		value, ok := reply.Lookup(field.key)
		if !ok {
			return nil, fmt.Errorf("no %s", field.key)
		}
		number, err := value.Uint64()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", field.key, err)
		}
		*field.dst = number
	}
	if info.Protocol != ProtocolVersion {
		return nil, fmt.Errorf("server replied with protocol %d, expected %d", info.Protocol, ProtocolVersion)
	}
	return info, nil
}

// Do sends one statement with its parameter frames and returns its reply;
// a server error is returned as a *ServerError.
func (c *Client) Do(statement string, parameters ...[]byte) (Value, error) {
	replies, err := c.Pipeline([]Request{{Statement: statement, Parameters: parameters}})
	if err != nil {
		return Value{}, err
	}
	if replies[0].Err != nil {
		return Value{}, replies[0].Err
	}
	return replies[0].Value, nil
}

// Pipeline sends every request before reading the replies, which come back
// in request order. A server error fails only its own request; an error
// returned here (framing, transport, a reply that does not parse) leaves
// the connection unusable.
func (c *Client) Pipeline(requests []Request) ([]Reply, error) {
	for _, request := range requests {
		if err := c.checkRequest(request); err != nil {
			return nil, err
		}
	}
	return c.roundTrip(requests)
}

// checkRequest refuses a request the server could not frame: a statement is
// one line, and the parameter frames must be exactly the `$` parameters the
// statement names, or the server would read the frames as statements.
func (c *Client) checkRequest(request Request) error {
	statement := request.Statement
	if strings.ContainsAny(statement, "\r\n") {
		return errors.New("a statement must be a single line (it contains CR or LF)")
	}
	if strings.TrimSpace(statement) == "" {
		return errors.New("empty statement")
	}
	if c.info != nil && uint64(len(statement)+2) > c.info.MaxLineBytes {
		return fmt.Errorf("the statement is %d bytes, the server takes lines of at most %d bytes", len(statement)+2, c.info.MaxLineBytes)
	}
	want := ParameterCount(statement)
	if want != len(request.Parameters) {
		return fmt.Errorf("the statement names %d parameter(s) ($1 … $n), %d value(s) given", want, len(request.Parameters))
	}
	if c.info != nil && uint64(want) > c.info.MaxParameters {
		return fmt.Errorf("the statement has %d parameters, the server takes at most %d", want, c.info.MaxParameters)
	}
	return nil
}

// ParameterCount counts the `$` outside quoted values, as the server does to
// decide whether parameter frames follow a line; CQL numbers parameters
// without gaps and uses each once.
func ParameterCount(statement string) int {
	quoted := false
	count := 0
	for i := 0; i < len(statement); i++ {
		switch statement[i] {
		case '\'':
			// A doubled quote inside a quoted value closes and reopens it,
			// which leaves the same state.
			quoted = !quoted
		case '$':
			if !quoted {
				count++
			}
		}
	}
	return count
}

func (c *Client) roundTrip(requests []Request) ([]Reply, error) {
	if c.conn == nil {
		return nil, errors.New("connection is closed")
	}
	if c.broken != nil {
		return nil, fmt.Errorf("connection is unusable after an earlier error: %w", c.broken)
	}
	replies, err := c.exchange(requests)
	if err != nil {
		c.broken = err
		return nil, err
	}
	return replies, nil
}

func (c *Client) exchange(requests []Request) ([]Reply, error) {
	if err := c.conn.SetDeadline(time.Now().Add(c.timeout)); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	for _, request := range requests {
		if err := writeRequest(c.writer, request); err != nil {
			return nil, err
		}
	}
	if err := c.writer.Flush(); err != nil {
		return nil, fmt.Errorf("send statement: %w", err)
	}
	replies := make([]Reply, 0, len(requests))
	for range requests {
		value, err := readReply(c.reader)
		var serverErr *ServerError
		switch {
		case errors.As(err, &serverErr):
			replies = append(replies, Reply{Err: serverErr})
		case err != nil:
			return nil, err
		default:
			replies = append(replies, Reply{Value: value})
		}
	}
	return replies, nil
}

func writeRequest(w *bufio.Writer, request Request) error {
	if _, err := w.WriteString(request.Statement + "\r\n"); err != nil {
		return fmt.Errorf("send statement: %w", err)
	}
	for _, frame := range request.Parameters {
		if _, err := w.WriteString(frameHeader(frame)); err != nil {
			return fmt.Errorf("send parameter: %w", err)
		}
		if frame == nil {
			continue
		}
		if _, err := w.Write(frame); err != nil {
			return fmt.Errorf("send parameter: %w", err)
		}
		if _, err := w.WriteString("\r\n"); err != nil {
			return fmt.Errorf("send parameter: %w", err)
		}
	}
	return nil
}

// frameHeader is `$<length>\r\n`, or `$-1\r\n` for NULL (a nil frame).
func frameHeader(frame []byte) string {
	if frame == nil {
		return "$-1\r\n"
	}
	return "$" + strconv.Itoa(len(frame)) + "\r\n"
}
