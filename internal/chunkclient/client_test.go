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
