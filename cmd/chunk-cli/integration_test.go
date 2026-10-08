package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// These tests run the CLI against a chunkdb server binary named by
// CHUNKDB_SERVER_BIN (built with TLS); they are skipped without one.

const testToken = "chunk-token"

type testServer struct {
	uri string
}

func startServer(t *testing.T, tls bool) *testServer {
	t.Helper()
	binary := os.Getenv("CHUNKDB_SERVER_BIN")
	if binary == "" {
		t.Skip("set CHUNKDB_SERVER_BIN to a chunkdb server binary to run the integration tests")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	scheme := "chunk"
	if tls {
		scheme = "chunks"
	}
	uri := scheme + "://" + testToken + "@127.0.0.1:" + strconv.Itoa(port) + "/"
	args := []string{"--listen-uri", uri, "--data-dir", t.TempDir(), "--durability", "relaxed", "--workers", "4", "--log-level", "warn"}
	if tls {
		cert, key := writeTLSFixture(t)
		args = append(args, "--tls-cert", cert, "--tls-key", key)
	}
	command := exec.Command(binary, args...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}
	t.Cleanup(func() {
		_ = command.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() {
			_ = command.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = command.Process.Kill()
			<-done
		}
	})

	address := "127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(15 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", address, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return &testServer{uri: uri}
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start on %s: %s", address, output.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func writeTLSFixture(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "chunkdb-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	return certPath, keyPath
}

// cli runs chunk-cli with the server's URI and returns its exit status,
// stdout and stderr.
func (s *testServer) cli(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(append([]string{"--uri", s.uri, "--tls-insecure"}, args...), strings.NewReader(stdin), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// ok runs a statement that must succeed and returns its output.
func (s *testServer) ok(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := s.cli(t, "", args...)
	if code != 0 || errOut != "" {
		t.Fatalf("%v: exit %d, stderr %q", args, code, errOut)
	}
	return out
}

func (s *testServer) fails(t *testing.T, want string, args ...string) {
	t.Helper()
	code, out, errOut := s.cli(t, "", args...)
	if code != 1 || out != "" || !strings.Contains(errOut, want) {
		t.Fatalf("%v: got exit %d, stdout %q, stderr %q; want an error with %q", args, code, out, errOut, want)
	}
}

const createWorld = "CREATE TABLE world (id u64 REQUIRED, light u4 DEFAULT 15, s i8, f f32 NULL, ok bool, " +
	"flags bits(5), name text(16) NULL, blob bytes(8)) CHUNK 2 x 2"

func TestCLIBlocks(t *testing.T) {
	s := startServer(t, false)
	if out := s.ok(t, "PING"); out != "PONG\n" {
		t.Fatalf("PING: %q", out)
	}
	if out := s.ok(t, createWorld); out != "OK\n" {
		t.Fatalf("CREATE TABLE: %q", out)
	}
	if out := s.ok(t, "SHOW", "TABLES"); out != "1) default\n2) world\n" {
		t.Fatalf("SHOW TABLES: %q", out)
	}
	version := strings.TrimSpace(s.ok(t, "SET BLOCK 1 0 IN world id = 18446744073709551615, s = -3, f = 1.5, ok = TRUE, "+
		"flags = b'10110', name = 'it''s', blob = x'000d0a'"))
	if _, err := strconv.ParseUint(version, 10, 64); err != nil {
		t.Fatalf("SET BLOCK printed %q", version)
	}
	want := "id = 18446744073709551615\nlight = 15\ns = -3\nf = 1.5\nok = true\nflags = b'10110'\nname = 'it''s'\nblob = x'000d0a'\n"
	if out := s.ok(t, "GET BLOCK 1 0 FROM world"); out != want {
		t.Fatalf("GET BLOCK: got %q, want %q", out, want)
	}
	if out := s.ok(t, "GET BLOCK 1 0 FROM world COLUMNS blob, id"); out != "blob = x'000d0a'\nid = 18446744073709551615\n" {
		t.Fatalf("GET BLOCK COLUMNS: %q", out)
	}
	wantJSON := `{"id":18446744073709551615,"light":15,"s":-3,"f":1.5,"ok":true,"flags":"10110","name":"it's","blob":"000d0a"}` + "\n"
	if out := s.ok(t, "--json", "GET BLOCK 1 0 FROM world"); out != wantJSON {
		t.Fatalf("GET BLOCK --json: got %q, want %q", out, wantJSON)
	}
	if out := s.ok(t, "GET BLOCK 5 5 FROM world"); out != "NULL\n" {
		t.Fatalf("absent block: %q", out)
	}
	if out := s.ok(t, "--json", "GET BLOCK 5 5 FROM world"); out != "null\n" {
		t.Fatalf("absent block --json: %q", out)
	}
	// NULL, inf and defaults of a new block.
	s.ok(t, "SET BLOCK 0 1 IN world id = 1, f = -inf, name = NULL")
	if out := s.ok(t, "GET BLOCK 0 1 FROM world COLUMNS f, name, light, blob"); out != "f = -inf\nname = NULL\nlight = 15\nblob = x''\n" {
		t.Fatalf("GET BLOCK 0 1: %q", out)
	}

	// IF VERSION: a stale version fails with the current one and changes
	// nothing.
	current := strings.TrimSpace(s.ok(t, "SET BLOCK 1 0 IN world light = 3"))
	s.fails(t, "VERSION_MISMATCH current="+current, "SET BLOCK 1 0 IN world light = 4 IF VERSION "+version)
	if out := s.ok(t, "GET BLOCK 1 0 FROM world COLUMNS light"); out != "light = 3\n" {
		t.Fatalf("after the mismatch: %q", out)
	}
	s.ok(t, "SET BLOCK 1 0 IN world light = 4 IF VERSION "+current)
	deleted := strings.TrimSpace(s.ok(t, "DELETE BLOCK 1 0 FROM world"))
	if deleted == current {
		t.Fatalf("DELETE BLOCK kept the version %s", deleted)
	}
	if out := s.ok(t, "GET BLOCK 1 0 FROM world"); out != "NULL\n" {
		t.Fatalf("deleted block: %q", out)
	}

	s.fails(t, "SYNTAX", "BOGUS")
	s.fails(t, "INVALID_ARGUMENT", "SET BLOCK 0 0 IN world id = 1, light = 16")
	s.fails(t, "NO_TABLE", "GET BLOCK 0 0 FROM missing")
	s.fails(t, "1 parameter(s)", "SET BLOCK 0 0 IN world name = $1")
}

func TestCLIChunksAndAreas(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, createWorld)
	s.ok(t, "SET BLOCK 1 0 IN world id = 5, name = 'hi', blob = x'00ff0d0a'")
	s.ok(t, "SET BLOCK 0 1 IN world id = 6, f = 0.5")
	s.ok(t, "SET BLOCK 4 4 IN world id = 7")

	out := s.ok(t, "GET CHUNK 0 0 FROM world")
	if !strings.HasPrefix(out, "version = ") || !strings.HasSuffix(out, "\nschema_version = 1\npresent = 2 of 4 blocks\n") {
		t.Fatalf("GET CHUNK: %q", out)
	}
	out = s.ok(t, "--blocks", "GET CHUNK 0 0 FROM world COLUMNS name, id, blob")
	want := "present = 2 of 4 blocks\nblock 1 0\n  name = 'hi'\n  id = 5\n  blob = x'00ff0d0a'\n" +
		"block 0 1\n  name = NULL\n  id = 6\n  blob = x''\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("GET CHUNK --blocks: got %q, want suffix %q", out, want)
	}
	out = s.ok(t, "--json", "--blocks", "GET CHUNK 0 0 FROM world COLUMNS f")
	var chunk struct {
		Version    uint64 `json:"version"`
		Present    int    `json:"present"`
		BlockCount int    `json:"block_count"`
		Blocks     []struct {
			X, Y   int64
			Values map[string]any
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(out), &chunk); err != nil {
		t.Fatalf("GET CHUNK --json: %v in %q", err, out)
	}
	if chunk.Version == 0 || chunk.Present != 2 || chunk.BlockCount != 4 || len(chunk.Blocks) != 2 ||
		chunk.Blocks[1].X != 0 || chunk.Blocks[1].Y != 1 || chunk.Blocks[1].Values["f"] != 0.5 || chunk.Blocks[0].Values["f"] != nil {
		t.Fatalf("GET CHUNK --json: %+v", chunk)
	}

	out = s.ok(t, "--blocks", "GET AREA 0 0 TO 2 2 FROM world COLUMNS id")
	want = "chunk 0 0\n  version = "
	if !strings.HasPrefix(out, want) || !strings.Contains(out, "  present = 2 of 4 blocks\n  block 1 0\n    id = 5\n  block 0 1\n    id = 6\n") ||
		!strings.Contains(out, "chunk 2 2\n") || !strings.HasSuffix(out, "  block 4 4\n    id = 7\n") {
		t.Fatalf("GET AREA: %q", out)
	}
	if out := s.ok(t, "GET AREA AROUND 20 20 RADIUS 1 FROM world"); out != "(empty)\n" {
		t.Fatalf("empty area: %q", out)
	}
	out = s.ok(t, "--json", "GET AREA AROUND 2 2 RADIUS 0 FROM world")
	if !strings.HasPrefix(out, `[{"cx":2,"cy":2,"version":`) || !strings.HasSuffix(out, `"present":1,"block_count":4}]`+"\n") {
		t.Fatalf("GET AREA --json: %q", out)
	}

	if out := s.ok(t, "SCAN CHUNKS FROM world LIMIT 1"); out != "chunks:\n  1) [0, 0]\nmore = true\n" {
		t.Fatalf("SCAN CHUNKS: %q", out)
	}
	if out := s.ok(t, "--json", "SCAN CHUNKS FROM world AFTER 0 0"); out != `{"chunks":[[2,2]],"more":false}`+"\n" {
		t.Fatalf("SCAN CHUNKS --json: %q", out)
	}

	// Dump a chunk to a file and write it back elsewhere, its text and
	// bytes values included.
	file := filepath.Join(t.TempDir(), "chunk.bin")
	out = s.ok(t, "--out", file, "GET CHUNK 0 0 FROM world")
	if !strings.HasPrefix(out, "wrote ") || !strings.HasSuffix(out, " bytes to "+file+"\n") {
		t.Fatalf("--out: %q", out)
	}
	written := strings.TrimSpace(s.ok(t, "--in", file, "SET CHUNK 5 5 IN world $1"))
	s.fails(t, "VERSION_MISMATCH current="+written, "--in", file, "SET CHUNK 5 5 IN world $1 IF VERSION 1")
	if out := s.ok(t, "GET BLOCK 11 10 FROM world COLUMNS id, name, blob"); out != "id = 5\nname = 'hi'\nblob = x'00ff0d0a'\n" {
		t.Fatalf("block of the loaded chunk: %q", out)
	}
	s.fails(t, "reply of bytes", "--out", file, "PING")
	s.fails(t, "no such file", "--in", filepath.Join(t.TempDir(), "missing"), "SET CHUNK 5 5 IN world $1")
	s.fails(t, "0 parameter(s)", "--in", file, "PING")

	// After the table's columns changed, the dumped form no longer fits.
	s.ok(t, "ALTER TABLE world ADD COLUMN extra u8")
	s.fails(t, "SCHEMA_MISMATCH current=2", "--in", file, "SET CHUNK 6 6 IN world $1")
}

func TestCLITablesAndAlter(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE t (a u8, label text(8) NULL) CHUNK 2 x 2 WITH durability_mode = 'fsync-wal'")
	out := s.ok(t, "DESCRIBE t")
	if !strings.HasPrefix(out, "table = t\nversion = 1\nchunk = 2 x 2\nlarge = ") ||
		!strings.Contains(out, "columns:\n  a u8\n  label text(8) NULL\noptions:\n  durability_mode = fsync-wal\n") {
		t.Fatalf("DESCRIBE: %q", out)
	}
	s.ok(t, "SET BLOCK 0 0 IN t a = 1, label = 'one'")

	// A column added after the block (which takes its DEFAULT): the next
	// reads decode with the new schema.
	s.ok(t, "ALTER TABLE t ADD COLUMN extra i16 NULL DEFAULT -2")
	if out := s.ok(t, "GET BLOCK 0 0 FROM t"); out != "a = 1\nlabel = 'one'\nextra = -2\n" {
		t.Fatalf("after ADD COLUMN: %q", out)
	}
	s.ok(t, "SET BLOCK 1 1 IN t a = 2")
	if out := s.ok(t, "--blocks", "GET CHUNK 0 0 FROM t"); !strings.HasSuffix(out, "block 0 0\n  a = 1\n  label = 'one'\n  extra = -2\n"+
		"block 1 1\n  a = 2\n  label = NULL\n  extra = -2\n") {
		t.Fatalf("GET CHUNK after ADD COLUMN: %q", out)
	}
	if out := s.ok(t, "DESCRIBE t"); !strings.Contains(out, "version = 2\n") || !strings.Contains(out, "  extra i16 NULL DEFAULT -2\n") {
		t.Fatalf("DESCRIBE after ADD COLUMN: %q", out)
	}
	s.ok(t, "ALTER TABLE t RENAME COLUMN label TO name")
	s.ok(t, "ALTER TABLE t DROP COLUMN extra")
	if out := s.ok(t, "GET BLOCK 1 1 FROM t"); out != "a = 2\nname = NULL\n" {
		t.Fatalf("after DROP COLUMN: %q", out)
	}
	// One text column: its values are decoded although the schema changed.
	if out := s.ok(t, "--blocks", "GET CHUNK 0 0 FROM t COLUMNS name"); !strings.HasSuffix(out, "block 0 0\n  name = 'one'\nblock 1 1\n  name = NULL\n") {
		t.Fatalf("GET CHUNK after RENAME: %q", out)
	}
	s.fails(t, "out of range 0..255", "SET BLOCK 0 0 IN t a = 300, name = 'x'")
	s.ok(t, "ALTER TABLE t ALTER COLUMN a TYPE u4 USING CLAMP")
	if out := s.ok(t, "GET BLOCK 1 1 FROM t COLUMNS a"); out != "a = 2\n" {
		t.Fatalf("after ALTER COLUMN TYPE: %q", out)
	}
	s.ok(t, "ALTER TABLE t SET checkpoint_updates = 10")
	if out := s.ok(t, "--json", "DESCRIBE t"); !strings.Contains(out, `"checkpoint_updates":10`) || !strings.Contains(out, `{"id":1,"name":"a","type":"u4","null":false,"required":false,"default":null}`) {
		t.Fatalf("DESCRIBE --json: %q", out)
	}
	s.ok(t, "DROP TABLE t")
	s.fails(t, "NO_TABLE", "DESCRIBE t")

	if out := s.ok(t, "FLUSH WAL"); out != "OK\n" {
		t.Fatalf("FLUSH WAL: %q", out)
	}
	if out := s.ok(t, "SHOW METRICS"); !strings.Contains(out, "# TYPE chunkdb_commands_total counter\n") {
		t.Fatalf("SHOW METRICS: %q", out)
	}
}

func TestCLITextColumnsAfterAlter(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE v (a u4, old text(4) NULL, b bytes(4)) CHUNK 2 x 2")
	s.ok(t, "SET BLOCK 0 0 IN v a = 1, old = 'gone', b = x'0102'")
	s.ok(t, "ALTER TABLE v DROP COLUMN old")
	s.ok(t, "ALTER TABLE v ADD COLUMN label text(8) NULL")
	s.ok(t, "ALTER TABLE v ADD COLUMN raw bytes(4) NULL")
	s.ok(t, "SET BLOCK 1 0 IN v a = 2, label = 'new', raw = x'0d0a'")
	s.ok(t, "SET BLOCK 0 1 IN v a = 3, b = x'ff', label = 'l3'")

	want := "schema_version = 4\npresent = 3 of 4 blocks\n" +
		"block 0 0\n  a = 1\n  b = x'0102'\n  label = NULL\n  raw = NULL\n" +
		"block 1 0\n  a = 2\n  b = x''\n  label = 'new'\n  raw = x'0d0a'\n" +
		"block 0 1\n  a = 3\n  b = x'ff'\n  label = 'l3'\n  raw = NULL\n"
	if out := s.ok(t, "--blocks", "GET CHUNK 0 0 FROM v"); !strings.HasSuffix(out, want) {
		t.Fatalf("GET CHUNK after ADD/DROP: got %q, want suffix %q", out, want)
	}
	want = "block 0 0\n  raw = NULL\n  label = NULL\nblock 1 0\n  raw = x'0d0a'\n  label = 'new'\nblock 0 1\n  raw = NULL\n  label = 'l3'\n"
	if out := s.ok(t, "--blocks", "GET CHUNK 0 0 FROM v COLUMNS raw, label"); !strings.HasSuffix(out, want) {
		t.Fatalf("GET CHUNK COLUMNS after ADD/DROP: got %q, want suffix %q", out, want)
	}
	if out := s.ok(t, "--json", "GET CHUNK 0 0 FROM v"); !strings.Contains(out, `,"schema_version":4,"present":3,`) {
		t.Fatalf("GET CHUNK --json: %q", out)
	}
	if out := s.ok(t, "--blocks", "GET AREA 0 0 TO 0 0 FROM v COLUMNS label"); !strings.Contains(out, "  schema_version = 4\n") ||
		!strings.Contains(out, "  block 1 0\n    label = 'new'\n") {
		t.Fatalf("GET AREA after ADD/DROP: %q", out)
	}
}

func TestCLIShell(t *testing.T) {
	s := startServer(t, false)
	file := filepath.Join(t.TempDir(), "chunk.bin")
	script := strings.Join([]string{
		"PING",
		"",
		"help",
		createWorld,
		"SET BLOCK 0 0 IN world id = 9, name = 'shell'",
		"--json GET BLOCK 0 0 FROM world COLUMNS id, name",
		"BOGUS",
		"--bogus PING",
		"SET BLOCK 0 0 IN world name = $1",
		"--out " + file + " GET CHUNK 0 0 FROM world",
		"--in " + file + " SET CHUNK 1 1 IN world $1",
		"--blocks GET CHUNK 1 1 FROM world COLUMNS name",
		"exit",
		"PING",
	}, "\n") + "\n"
	code, out, errOut := s.cli(t, script, "shell")
	if code != 0 {
		t.Fatalf("shell exited %d: %q", code, errOut)
	}
	for _, want := range []string{
		"chunk> PONG\nchunk> chunk> Type one CQL statement per line",
		"chunk> OK\n",
		`chunk> {"id":9,"name":"shell"}` + "\n",
		"chunk> wrote ",
		"block 2 2\n  name = 'shell'\nchunk> ",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in %q", want, out)
		}
	}
	if strings.Count(out, "PONG") != 1 {
		t.Fatalf("the shell went on after exit: %q", out)
	}
	for _, want := range []string{"error: SYNTAX column 1: unknown statement 'BOGUS'\n", `error: unknown option "--bogus"`, "1 parameter(s)"} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("expected %q in stderr %q", want, errOut)
		}
	}

	// --json for the whole shell.
	_, out, _ = s.cli(t, "PING\nquit\n", "--json", "shell")
	if out != "chunk> \"PONG\"\nchunk> " {
		t.Fatalf("shell --json: %q", out)
	}
}

func TestCLIConnection(t *testing.T) {
	s := startServer(t, false)
	wrong := strings.Replace(s.uri, testToken, "wrong", 1)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--uri", wrong, "PING"}, strings.NewReader(""), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "connecting failed: AUTH_FAILED") {
		t.Fatalf("wrong token: %d %q", code, stderr.String())
	}
	stderr.Reset()
	if code := run([]string{"--uri", wrong, "--token", testToken, "PING"}, strings.NewReader(""), &stdout, &stderr); code != 0 || stdout.String() != "PONG\n" {
		t.Fatalf("--token: %d %q %q", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	anonymous := strings.Replace(s.uri, testToken+"@", "", 1)
	if code := run([]string{"--uri", anonymous, "PING"}, strings.NewReader(""), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "AUTH_REQUIRED") {
		t.Fatalf("no token: %d %q", code, stderr.String())
	}

	secure := startServer(t, true)
	if out := secure.ok(t, "PING"); out != "PONG\n" {
		t.Fatalf("TLS PING: %q", out)
	}
	secure.ok(t, createWorld)
	secure.ok(t, "SET BLOCK 0 0 IN world id = 1, blob = x'0d0a00'")
	if out := secure.ok(t, "GET BLOCK 0 0 FROM world COLUMNS blob"); out != "blob = x'0d0a00'\n" {
		t.Fatalf("TLS GET BLOCK: %q", out)
	}
	stderr.Reset()
	if code := run([]string{"--uri", secure.uri, "PING"}, strings.NewReader(""), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "certificate") {
		t.Fatalf("TLS without --tls-insecure: %d %q", code, stderr.String())
	}
}
