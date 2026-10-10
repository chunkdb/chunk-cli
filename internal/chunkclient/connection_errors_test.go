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
