package chunkclient

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

func TestConnectionErrorAdvicePreservesCause(t *testing.T) {
	for _, tc := range []struct {
		name   string
		cause  error
		secure bool
		want   string
	}{
		{"refused", syscall.ECONNREFUSED, false, "connect host:4242: connection refused; start the server and check the host and port: "},
		{"timeout", context.DeadlineExceeded, false, "connect host:4242: timed out; check server availability or increase --timeout; a sent write may already have applied: "},
		{"tls_mismatch", tls.RecordHeaderError{Msg: "bad record"}, true, "connect host:4242: TLS handshake failed; use chunks:// for a TLS server or chunk:// for a plain server: "},
		{"tls_certificate", errors.New("certificate invalid"), true, "connect host:4242: TLS connection failed; check the server certificate and chunks:// endpoint: "},
		{"closed", io.EOF, false, "connect host:4242: connection closed before the reply; check chunk:// versus chunks:// and the server logs: "},
		{"reset", syscall.ECONNRESET, false, "connect host:4242: connection reset by the server; check chunk:// versus chunks:// and the server logs: "},
		{"canceled", context.Canceled, true, "connect host:4242: "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := connectionError("connect", "host:4242", tc.secure, tc.cause)
			if err.Error() != tc.want+tc.cause.Error() || !errors.Is(err, tc.cause) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestDialRefusedAdvice(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	uri, err := chunkuri.Parse("chunk://" + address + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Dial(Config{URI: uri, Timeout: time.Second})
	if !errors.Is(err, syscall.ECONNREFUSED) || !strings.HasPrefix(err.Error(), "connect "+address+": connection refused; start the server and check the host and port: ") {
		t.Fatalf("got %v", err)
	}
}

func TestDialTLSMismatchAdvice(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		header := make([]byte, 5)
		if _, err := io.ReadFull(conn, header); err != nil {
			done <- err
			return
		}
		_, err = fmt.Fprint(conn, "HTTP/1.1 400 Bad Request\r\n\r\n")
		done <- err
	}()
	uri, err := chunkuri.Parse("chunks://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Dial(Config{URI: uri, Timeout: time.Second})
	var record tls.RecordHeaderError
	if !errors.As(err, &record) || !strings.Contains(err.Error(), "TLS handshake failed; use chunks:// for a TLS server or chunk:// for a plain server") {
		t.Fatalf("got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestClosedReplyAdvice(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	client := &Client{conn: clientConn, reader: bufio.NewReader(clientConn), writer: bufio.NewWriter(clientConn), timeout: time.Second}
	defer client.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(serverConn)
		_, _ = r.ReadString('\n')
		serverConn.Close()
	}()
	_, err := client.Hello(Login{})
	if !errors.Is(err, io.EOF) || !strings.Contains(err.Error(), "connection closed before the reply; check chunk:// versus chunks:// and the server logs") {
		t.Fatalf("got %v", err)
	}
	<-done
}

func TestResetReplyAdvice(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		// A TLS server that reads a plain request closes with unread data,
		// which resets the connection.
		_, err = conn.Read(make([]byte, 1))
		conn.(*net.TCPConn).SetLinger(0)
		conn.Close()
		done <- err
	}()
	uri, err := chunkuri.Parse("chunk://" + listener.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	client, err := Dial(Config{URI: uri, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, err = client.Hello(Login{})
	if !errors.Is(err, syscall.ECONNRESET) || !strings.Contains(err.Error(), "connection reset by the server; check chunk:// versus chunks:// and the server logs") {
		t.Fatalf("got %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// lostReplyClient answers each statement with its reply in replies, in
// order, and closes the connection at the first statement without one.
func lostReplyClient(t *testing.T, replies ...string) *Client {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	client := &Client{conn: clientConn, reader: bufio.NewReader(clientConn), writer: bufio.NewWriter(clientConn), timeout: 2 * time.Second}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer serverConn.Close()
		r := bufio.NewReader(serverConn)
		for _, reply := range append(replies, "") {
			if _, err := r.ReadString('\n'); err != nil || reply == "" {
				return
			}
			if _, err := io.WriteString(serverConn, reply); err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = client.Close()
		<-done
	})
	return client
}

func TestLostReplyIsAnUnknownOutcome(t *testing.T) {
	var unknown *OutcomeUnknownError
	// Outside a transaction a statement may have applied.
	_, err := lostReplyClient(t).Do("SET BLOCK 0 0 IN world v = 1")
	if !errors.As(err, &unknown) || unknown.RetryClass() != "unknown_outcome" || unknown.ExitCode() != 5 || !errors.Is(err, io.EOF) ||
		!strings.Contains(err.Error(), "the outcome is unknown") {
		t.Fatalf("outside a transaction: %v", err)
	}

	// Before COMMIT the server rolls the transaction back.
	client := lostReplyClient(t, "+OK\r\n")
	if _, err := client.Do("BEGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do("SET BLOCK 0 0 IN world v = 1"); err == nil || errors.As(err, &unknown) || !errors.Is(err, io.EOF) {
		t.Fatalf("inside a transaction: %v", err)
	}

	// COMMIT may have applied, also when it is pipelined with the rest.
	client = lostReplyClient(t, "+OK\r\n", "_\r\n")
	if _, err := client.Do("begin"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do("SET BLOCK 0 0 IN world v = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do("COMMIT"); !errors.As(err, &unknown) {
		t.Fatalf("COMMIT: %v", err)
	}
	_, err = lostReplyClient(t).Pipeline([]Request{{Statement: "BEGIN"}, {Statement: "SET BLOCK 0 0 IN world v = 1"}, {Statement: "COMMIT"}})
	if !errors.As(err, &unknown) {
		t.Fatalf("pipelined COMMIT: %v", err)
	}
	_, err = lostReplyClient(t).Pipeline([]Request{{Statement: "BEGIN"}, {Statement: "SET BLOCK 0 0 IN world v = 1"}})
	if err == nil || errors.As(err, &unknown) {
		t.Fatalf("pipelined statements before COMMIT: %v", err)
	}

	// A login applies nothing.
	if _, err := lostReplyClient(t).Hello(Login{}); err == nil || errors.As(err, &unknown) {
		t.Fatalf("HELLO: %v", err)
	}
}

func TestReplyLinesAreCapped(t *testing.T) {
	long := "+" + strings.Repeat("a", maxReplyLineBytes) + "\r\n"
	if _, err := readReply(bufio.NewReader(strings.NewReader(long))); err == nil || !strings.Contains(err.Error(), "reply line exceeds 1048576 bytes") {
		t.Fatalf("got %v", err)
	}
	fits := "+" + strings.Repeat("a", maxReplyLineBytes-3) + "\r\n"
	if value, err := readReply(bufio.NewReader(strings.NewReader(fits))); err != nil || len(value.Text) != maxReplyLineBytes-3 {
		t.Fatalf("a line of the limit: %d bytes, %v", len(value.Text), err)
	}
	limits := &ReplyLimits{MaxBytes: 4 << 20, MaxAreaChunks: 1}
	if _, err := readReplyLimited(bufio.NewReader(strings.NewReader(long)), limits); err == nil || !strings.Contains(err.Error(), "reply line exceeds") {
		t.Fatalf("budgeted: %v", err)
	}
}
