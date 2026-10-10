package chunkclient

import (
	"bufio"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

// scriptedClient is a client whose server answers with the given bytes and
// records what the client sent.
type scriptedClient struct {
	*Client
	mu   sync.Mutex
	sent bytes.Buffer
	done chan struct{}
}

func newScriptedClient(t *testing.T, replies string) *scriptedClient {
	t.Helper()
	clientConn, serverConn := net.Pipe()
	sc := &scriptedClient{
		Client: &Client{
			conn:    clientConn,
			reader:  bufio.NewReader(clientConn),
			writer:  bufio.NewWriter(clientConn),
			timeout: 2 * time.Second,
		},
		done: make(chan struct{}),
	}
	go func() {
		defer close(sc.done)
		buf := make([]byte, 4096)
		for {
			n, err := serverConn.Read(buf)
			sc.mu.Lock()
			sc.sent.Write(buf[:n])
			sc.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		_, _ = io.WriteString(serverConn, replies)
	}()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
		<-sc.done
	})
	return sc
}

func (sc *scriptedClient) sentText() string {
	_ = sc.conn.Close()
	<-sc.done
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return sc.sent.String()
}

func TestReadReplyTypes(t *testing.T) {
	cases := []struct {
		reply string
		check func(Value) bool
	}{
		{"+PONG\r\n", func(v Value) bool { return v.Kind == KindSimple && v.Text == "PONG" }},
		{":-12\r\n", func(v Value) bool { n, err := v.Int64(); return err == nil && n == -12 }},
		{":18446744073709551615\r\n", func(v Value) bool {
			n, err := v.Uint64()
			_, errSigned := v.Int64()
			return err == nil && n == math.MaxUint64 && errSigned != nil && v.Text == "18446744073709551615"
		}},
		{",1.5\r\n", func(v Value) bool { f, err := v.Float64(); return err == nil && f == 1.5 }},
		{",inf\r\n", func(v Value) bool { f, err := v.Float64(); return err == nil && math.IsInf(f, 1) }},
		{",-inf\r\n", func(v Value) bool { f, err := v.Float64(); return err == nil && math.IsInf(f, -1) }},
		{",nan\r\n", func(v Value) bool { f, err := v.Float64(); return err == nil && math.IsNaN(f) }},
		{"#t\r\n", func(v Value) bool { return v.Kind == KindBool && v.Bool }},
		{"#f\r\n", func(v Value) bool { return v.Kind == KindBool && !v.Bool }},
		{"_\r\n", func(v Value) bool { return v.Kind == KindNull }},
		{"$4\r\na\r\n\x00\r\n", func(v Value) bool { return v.Kind == KindBulk && string(v.Bulk) == "a\r\n\x00" }},
		{"$0\r\n\r\n", func(v Value) bool { return v.Kind == KindBulk && len(v.Bulk) == 0 }},
		{"*0\r\n", func(v Value) bool { return v.Kind == KindArray && len(v.Items) == 0 }},
		{"*3\r\n:1\r\n_\r\n*1\r\n$1\r\nx\r\n", func(v Value) bool {
			return len(v.Items) == 3 && v.Items[0].Text == "1" && v.Items[1].Kind == KindNull &&
				string(v.Items[2].Items[0].Bulk) == "x"
		}},
		{"%2\r\n$6\r\nchunks\r\n*0\r\n$4\r\nmore\r\n#t\r\n", func(v Value) bool {
			more, ok := v.Lookup("more")
			chunks, okChunks := v.Lookup("chunks")
			return v.Kind == KindMap && ok && more.Bool && okChunks && chunks.Kind == KindArray
		}},
	}
	for _, tc := range cases {
		value, err := readReply(bufio.NewReader(strings.NewReader(tc.reply)))
		if err != nil {
			t.Errorf("%q: %v", tc.reply, err)
			continue
		}
		if !tc.check(value) {
			t.Errorf("%q: unexpected value %+v", tc.reply, value)
		}
	}
}

func TestReadReplyRefusesMalformed(t *testing.T) {
	for _, reply := range []string{
		"", "\r\n", "+OK\n", ":12a\r\n", ":99999999999999999999\r\n", ",Inf\r\n", ",0x1p2\r\n", ",\r\n",
		"#x\r\n", "_x\r\n", "$-1\r\n", "$3\r\nab\r\n", "$2\r\nabcd", "*-1\r\n", "*2\r\n:1\r\n",
		"*1\r\n-ERR X y\r\n", "!3\r\nabc\r\n", "%1\r\n$1\r\nk\r\n",
	} {
		if value, err := readReply(bufio.NewReader(strings.NewReader(reply))); err == nil {
			t.Errorf("%q: expected an error, got %+v", reply, value)
		}
	}
	deep := strings.Repeat("*1\r\n", maxNesting+2) + ":1\r\n"
	if _, err := readReply(bufio.NewReader(strings.NewReader(deep))); err == nil {
		t.Error("expected an error for deep nesting")
	}
}

func TestServerErrorCodes(t *testing.T) {
	sc := newScriptedClient(t, "-ERR VERSION_MISMATCH current=1043\r\n-ERR SYNTAX column 1: unknown statement 'X'\r\n")
	_, err := sc.Do("SET BLOCK 0 0 IN t a = 1 IF VERSION 1")
	var serverErr *ServerError
	if !errors.As(err, &serverErr) || serverErr.Code != "VERSION_MISMATCH" {
		t.Fatalf("got %v", err)
	}
	if current, ok := serverErr.CurrentVersion(); !ok || current != 1043 {
		t.Fatalf("current version %d, %v", current, ok)
	}
	if err.Error() != "VERSION_MISMATCH current=1043" {
		t.Fatalf("message %q", err.Error())
	}
	_, err = sc.Do("X")
	if !errors.As(err, &serverErr) || serverErr.Code != "SYNTAX" || serverErr.Message != "column 1: unknown statement 'X'" {
		t.Fatalf("got %#v", err)
	}
	if _, ok := serverErr.CurrentVersion(); ok {
		t.Fatal("a SYNTAX error has no current version")
	}
	if sc.Broken() != nil {
		t.Fatalf("server errors must not break the connection: %v", sc.Broken())
	}
}

func TestParameterFrames(t *testing.T) {
	sc := newScriptedClient(t, ":7\r\n")
	value, err := sc.Do("SET BLOCK 10 4 IN world sign = $1, chest = $2, blob = $3", []byte("hello"), nil, []byte("a\r\n\x00"))
	if err != nil || value.Text != "7" {
		t.Fatalf("got %+v, %v", value, err)
	}
	want := "SET BLOCK 10 4 IN world sign = $1, chest = $2, blob = $3\r\n$5\r\nhello\r\n$-1\r\n$4\r\na\r\n\x00\r\n"
	if got := sc.sentText(); got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestRequestChecks(t *testing.T) {
	sc := newScriptedClient(t, "")
	sc.info = &ServerInfo{Protocol: 3, MaxLineBytes: 64, MaxParameters: 2}
	for _, tc := range []struct {
		statement  string
		parameters [][]byte
		want       string
	}{
		{"PING\r\nPING", nil, "single line"},
		{"GET BLOCK 0 0 FROM t\n", nil, "single line"},
		{"  ", nil, "empty statement"},
		{"SET CHUNK 0 0 IN t $1", nil, "1 parameter(s)"},
		{"PING", [][]byte{{1}}, "0 parameter(s)"},
		{"SET BLOCK 0 0 IN t a = '$1'", [][]byte{{1}}, "0 parameter(s)"},
		{"SET BLOCK 0 0 IN t a = $1, b = $2, c = $3", make([][]byte, 3), "at most 2"},
		{"SET BLOCK 0 0 IN t a = '" + strings.Repeat("x", 64) + "'", nil, "at most 64 bytes"},
	} {
		_, err := sc.Do(tc.statement, tc.parameters...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: got %v, want %q", tc.statement, err, tc.want)
		}
	}
	if got := sc.sentText(); got != "" {
		t.Fatalf("refused requests must send nothing, sent %q", got)
	}
}

func TestParameterCount(t *testing.T) {
	for statement, want := range map[string]int{
		"PING":                                  0,
		"SET CHUNK 0 0 IN t $1":                 1,
		"SET BLOCK 0 0 IN t a = $1, b = $2":     2,
		"SET BLOCK 0 0 IN t a = 'it''s $1'":     0,
		"SET BLOCK 0 0 IN t a = 'x', b = $1":    1,
		"SET BLOCK 0 0 IN t a = x'00', b = $1 ": 1,
	} {
		if got := ParameterCount(statement); got != want {
			t.Errorf("%q: got %d, want %d", statement, got, want)
		}
	}
}

func TestPipelineKeepsOrder(t *testing.T) {
	sc := newScriptedClient(t, "+PONG\r\n-ERR NO_TABLE table 'x' does not exist\r\n:5\r\n")
	replies, err := sc.Pipeline([]Request{
		{Statement: "PING"},
		{Statement: "DESCRIBE x"},
		{Statement: "SET CHUNK 0 0 IN t $1", Parameters: [][]byte{{9}}},
	})
	if err != nil {
		t.Fatalf("pipeline: %v", err)
	}
	if replies[0].Value.Text != "PONG" || replies[1].Err == nil || replies[1].Err.Code != "NO_TABLE" || replies[2].Value.Text != "5" {
		t.Fatalf("unexpected replies %+v", replies)
	}
	if got, want := sc.sentText(), "PING\r\nDESCRIBE x\r\nSET CHUNK 0 0 IN t $1\r\n$1\r\n\x09\r\n"; got != want {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestMalformedReplyBreaksConnection(t *testing.T) {
	sc := newScriptedClient(t, "?what\r\n+PONG\r\n")
	if _, err := sc.Do("PING"); err == nil {
		t.Fatal("expected an error")
	}
	if sc.Broken() == nil {
		t.Fatal("expected the connection to be broken")
	}
	if _, err := sc.Do("PING"); err == nil || !strings.Contains(err.Error(), "unusable") {
		t.Fatalf("got %v", err)
	}
}

const helloFields = "$8\r\nprotocol\r\n:3\r\n$14\r\nserver_version\r\n$5\r\n2.0.0\r\n$14\r\nmax_line_bytes\r\n:65536\r\n" +
	"$14\r\nmax_parameters\r\n:65535\r\n$15\r\nmax_area_chunks\r\n:256\r\n$18\r\nmax_response_bytes\r\n:67108864\r\n" +
	"$14\r\nmax_scan_limit\r\n:1024\r\n$16\r\nserver_signature\r\n"

// helloReply is the HELLO map of a login without a user.
const helloReply = "%8\r\n" + helloFields + "_\r\n"

// helloReplySigned is the HELLO map of a login, with its server signature.
func helloReplySigned(signature string) string {
	return "%8\r\n" + helloFields + fmt.Sprintf("$%d\r\n%s\r\n", len(signature), signature)
}

func TestHello(t *testing.T) {
	sc := newScriptedClient(t, helloReply)
	info, err := sc.Hello(Login{})
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	want := ServerInfo{Protocol: 3, ServerVersion: "2.0.0", MaxLineBytes: 65536, MaxParameters: 65535,
		MaxAreaChunks: 256, MaxResponseBytes: 67108864, MaxScanLimit: 1024}
	if *info != want || sc.Info() != info {
		t.Fatalf("got %+v", info)
	}
	if got := sc.sentText(); got != "HELLO 3\r\n" {
		t.Fatalf("sent %q", got)
	}
}

func TestHelloRefusals(t *testing.T) {
	cases := []struct {
		reply, user, want string
		is                error
	}{
		{"-ERR PROTOCOL expected HELLO 2\r\n", "", "older chunkdb protocol", nil},
		{"-ERR UNKNOWN_COMMAND HELLO\r\n", "", "chunkdb 1.x", nil},
		{"-ERR AUTH_REQUIRED use AUTH <token>\r\n", "bot", "chunkdb 1.x", nil},
		{"-ERR AUTH_REQUIRED use HELLO 3 USER <name> $1\r\n", "", "AUTH_REQUIRED", ErrAuthRequired},
		{"-ERR AUTH_FAILED temporary auth ban\r\n", "bot", "AUTH_FAILED", ErrAuthFailed},
		{"+OK\r\n", "", "expected a map", nil},
		{"+OK\r\n", "bot", "expected +SCRAM", nil},
		{"+SCRAM r=x,s=AAAA,i=4096\r\n", "bot", "does not continue the client nonce", nil},
		{strings.Replace(helloReply, ":3\r\n", ":4\r\n", 1), "", "protocol 4", nil},
		{strings.Replace(helloReply, "max_scan_limit", "max_scan_limiX", 1), "", "no max_scan_limit", nil},
		{"%7\r\n" + strings.TrimSuffix(helloFields, "$16\r\nserver_signature\r\n"), "", "no server_signature", nil},
	}
	for _, tc := range cases {
		sc := newScriptedClient(t, tc.reply)
		_, err := sc.Hello(Login{User: tc.user, Password: "pw"})
		if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.is != nil && !errors.Is(err, tc.is)) {
			t.Errorf("reply %q: got %v, want %q", tc.reply, err, tc.want)
		}
	}
	for _, user := range []string{"a b", "a,b", "a=b", "a\x00"} {
		sc := newScriptedClient(t, helloReply)
		if _, err := sc.Hello(Login{User: user, Password: "pw"}); err == nil || !strings.Contains(err.Error(), "must not contain") {
			t.Errorf("user %q: got %v", user, err)
		}
	}
}

// helloServer accepts one connection, answers HELLO and PING, and closes.
func helloServer(t *testing.T, ln net.Listener) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		reader := bufio.NewReader(conn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "HELLO 3"):
				_, _ = io.WriteString(conn, helloReply)
			case line == "PING\r\n":
				_, _ = io.WriteString(conn, "+PONG\r\n")
			default:
				_, _ = io.WriteString(conn, "-ERR SYNTAX unexpected\r\n")
			}
		}
	}()
	return done
}

func dialAndPing(t *testing.T, cfg Config) {
	t.Helper()
	c, err := Dial(cfg)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.Hello(Login{}); err != nil {
		t.Fatalf("hello: %v", err)
	}
	value, err := c.Do("PING")
	if err != nil || value.Kind != KindSimple || value.Text != "PONG" {
		t.Fatalf("got %+v, %v", value, err)
	}
}

func TestDialChunk(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	done := helloServer(t, ln)

	parsed, err := chunkuri.Parse(fmt.Sprintf("chunk://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	dialAndPing(t, Config{URI: parsed, Timeout: 2 * time.Second})
	<-done
}

func TestDialChunks(t *testing.T) {
	cert := mustSelfSignedCert(t)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	defer ln.Close()
	done := helloServer(t, ln)

	parsed, err := chunkuri.Parse(fmt.Sprintf("chunks://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	dialAndPing(t, Config{URI: parsed, Timeout: 2 * time.Second, TLSInsecure: true})
	<-done
}

func TestTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	parsed, err := chunkuri.Parse(fmt.Sprintf("chunk://127.0.0.1:%d/", ln.Addr().(*net.TCPAddr).Port))
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	c, err := Dial(Config{URI: parsed, Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	defer func() { (<-accepted).Close() }()
	start := time.Now()
	_, err = c.Hello(Login{})
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected a timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("the timeout took %v", elapsed)
	}
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
