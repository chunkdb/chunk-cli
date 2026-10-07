package chunkclient

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkuri"
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

func TestCommandArrayResponse(t *testing.T) {
	c := newPipeClient(t, func(server net.Conn) {
		buf := make([]byte, 64)
		_, _ = server.Read(buf)
		// MGET reply: *N then N bulk items
		_, _ = server.Write([]byte("*3\r\n$4\r\n1010\r\n$4\r\n0000\r\n$4\r\n1111\r\n"))
	})
	defer c.Close()

	resp, err := c.Command("MGET 0 0 1 0 2 0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Kind != ResponseArray {
		t.Fatalf("expected array response, got: %#v", resp)
	}
	want := []string{"1010", "0000", "1111"}
	if len(resp.Array) != len(want) {
		t.Fatalf("expected %d items, got %d: %#v", len(want), len(resp.Array), resp.Array)
	}
	for i, w := range want {
		if string(resp.Array[i]) != w {
			t.Fatalf("item %d: expected %q, got %q", i, w, string(resp.Array[i]))
		}
	}
}

func TestCommandEmptyArrayResponse(t *testing.T) {
	c := newPipeClient(t, func(server net.Conn) {
		buf := make([]byte, 64)
		_, _ = server.Read(buf)
		_, _ = server.Write([]byte("*0\r\n"))
	})
	defer c.Close()

	resp, err := c.Command("MGET")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Kind != ResponseArray || len(resp.Array) != 0 {
		t.Fatalf("expected empty array response, got: %#v", resp)
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

func TestDialChunkAndCommand(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		line, _ := bufio.NewReader(conn).ReadString('\n')
		if strings.HasPrefix(line, "PING") {
			_, _ = conn.Write([]byte("+PONG\r\n"))
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	parsed, err := chunkuri.Parse(fmt.Sprintf("chunk://127.0.0.1:%d/", addr.Port))
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}

	c, err := Dial(Config{URI: parsed, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	resp, err := c.Command("PING")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if resp.Kind != ResponseSimple || resp.Simple != "PONG" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	<-done
}

func TestDialChunksAndCommand(t *testing.T) {
	cert := mustSelfSignedCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	defer ln.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		line, _ := bufio.NewReader(conn).ReadString('\n')
		if strings.HasPrefix(line, "PING") {
			_, _ = conn.Write([]byte("+PONG\r\n"))
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	parsed, err := chunkuri.Parse(fmt.Sprintf("chunks://127.0.0.1:%d/", addr.Port))
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}

	c, err := Dial(Config{URI: parsed, Timeout: 2 * time.Second, TLSInsecure: true})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()

	resp, err := c.Command("PING")
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	if resp.Kind != ResponseSimple || resp.Simple != "PONG" {
		t.Fatalf("unexpected response: %#v", resp)
	}

	<-done
}

func mustSelfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "127.0.0.1",
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}
}

func TestCommandNullResponses(t *testing.T) {
	c := newPipeClient(t, func(server net.Conn) {
		reader := bufio.NewReader(server)
		_, _ = reader.ReadString('\n')
		_, _ = server.Write([]byte("$-1\r\n"))
		_, _ = reader.ReadString('\n')
		_, _ = server.Write([]byte("*3\r\n$4\r\n1010\r\n$-1\r\n$0\r\n\r\n"))
	})
	defer c.Close()

	resp, err := c.Command("GET 0 0")
	if err != nil || resp.Kind != ResponseNull {
		t.Fatalf("expected a null response, got %#v, %v", resp, err)
	}
	resp, err = c.Command("MGET 0 0 1 0 2 0")
	if err != nil || resp.Kind != ResponseArray || len(resp.Array) != 3 {
		t.Fatalf("unexpected response: %#v, %v", resp, err)
	}
	if string(resp.Array[0]) != "1010" || resp.Array[1] != nil || resp.Array[2] == nil || len(resp.Array[2]) != 0 {
		t.Fatalf("a null item must be nil and an empty one non-nil: %#v", resp.Array)
	}
}

func TestHello(t *testing.T) {
	sent := make(chan string, 1)
	c := newPipeClient(t, func(server net.Conn) {
		line, _ := bufio.NewReader(server).ReadString('\n')
		sent <- line
		body := "protocol=2\nserver_version=test\ntable=terrain\nblock_bits=4\n"
		_, _ = fmt.Fprintf(server, "$%d\r\n%s\r\n", len(body), body)
	})
	defer c.Close()

	info, err := c.Hello("secret", "terrain")
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	if got := <-sent; got != "HELLO 2 AUTH secret TABLE terrain\r\n" {
		t.Fatalf("sent %q", got)
	}
	if info["table"] != "terrain" || info["block_bits"] != "4" || info["server_version"] != "test" {
		t.Fatalf("unexpected info %v", info)
	}
}

func TestHelloRefusals(t *testing.T) {
	cases := map[string]string{
		"-ERR UNKNOWN_COMMAND HELLO\r\n":     "does not speak protocol 2",
		"-ERR AUTH_FAILED invalid token\r\n": "AUTH_FAILED",
		"$11\r\nprotocol=3\n\r\n":            `protocol "3"`,
		"+OK\r\n":                            "expected bulk HELLO reply",
	}
	for reply, want := range cases {
		c := newPipeClient(t, func(server net.Conn) {
			_, _ = bufio.NewReader(server).ReadString('\n')
			_, _ = server.Write([]byte(reply))
		})
		_, err := c.Hello("", "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("reply %q: got %v, want %q", reply, err, want)
		}
		_ = c.Close()
	}

	// A 1.x server that requires a token answers AUTH_REQUIRED even to a
	// HELLO with a token; without a token the reply stays an auth error.
	for token, want := range map[string]string{"tok": "does not speak protocol 2", "": "AUTH_REQUIRED"} {
		c := newPipeClient(t, func(server net.Conn) {
			_, _ = bufio.NewReader(server).ReadString('\n')
			_, _ = server.Write([]byte("-ERR AUTH_REQUIRED use AUTH <token>\r\n"))
		})
		_, err := c.Hello(token, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("token %q: got %v, want %q", token, err, want)
		}
		_ = c.Close()
	}
}
