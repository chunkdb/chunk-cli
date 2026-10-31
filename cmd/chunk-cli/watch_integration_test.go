package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWatchProcess runs the CLI in a subprocess so SIGINT follows the actual
// signal handling path, including the final process exit status.
func TestWatchProcess(t *testing.T) {
	if os.Getenv("CHUNKCLI_WATCH_PROCESS") != "1" {
		return
	}
	// The owned child installs only its fixture CA; certificate and hostname
	// validation remain enabled without changing production TLS options.
	if caFile := os.Getenv("CHUNKCLI_WATCH_CA"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatal("invalid owned WATCH CA")
		}
		x509.SetFallbackRoots(roots)
	}
	for i, arg := range os.Args {
		if arg == "--" {
			os.Exit(run(os.Args[i+1:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	os.Exit(2)
}

type watchProcess struct {
	command  *exec.Cmd
	lines    chan string
	done     chan error
	stderr   bytes.Buffer
	finished bool
}

func startWatchProcess(t *testing.T, uri string, args ...string) *watchProcess {
	return startWatchProcessTrusted(t, uri, "", args...)
}

func startWatchProcessTrusted(t *testing.T, uri, caFile string, args ...string) *watchProcess {
	t.Helper()
	p := &watchProcess{lines: make(chan string, 64), done: make(chan error, 1)}
	global := []string{"-test.run=^TestWatchProcess$", "--", "--uri", uri}
	if caFile == "" {
		global = append(global, "--tls-insecure")
	}
	all := append(append(global, "watch", "world"), args...)
	p.command = exec.Command(os.Args[0], all...)
	p.command.Env = append(os.Environ(), "CHUNKCLI_WATCH_PROCESS=1")
	if caFile != "" {
		p.command.Env = append(p.command.Env, "CHUNKCLI_WATCH_CA="+caFile, "GODEBUG="+os.Getenv("GODEBUG")+",x509usefallbackroots=1")
	}
	p.command.Stderr = &p.stderr
	out, err := p.command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		scanner := bufio.NewScanner(out)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			p.lines <- scanner.Text()
		}
		close(p.lines)
		p.done <- p.command.Wait()
	}()
	t.Cleanup(func() {
		if p.finished {
			return
		}
		_ = p.command.Process.Signal(os.Interrupt)
		_ = drainWatchFixture(p.lines, p.done, time.Now().Add(5*time.Second), nil, func() { p.finished = true })
		if !p.finished {
			_ = p.command.Process.Kill()
			_ = drainWatchFixture(p.lines, p.done, time.Now().Add(5*time.Second), nil, func() { p.finished = true })
		}
		if !p.finished {
			t.Error("WATCH cleanup timed out reaping owned child")
		}
	})
	return p
}
func (p *watchProcess) line(t *testing.T) string {
	t.Helper()
	select {
	case line, ok := <-p.lines:
		if !ok {
			err := <-p.done
			p.finished = true
			t.Fatalf("watch stopped: %v; %s", err, p.stderr.String())
		}
		return line
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for watch output")
		return ""
	}
}
func (p *watchProcess) interrupt(t *testing.T, observers ...func(string) error) {
	t.Helper()
	if err := p.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	observe := func(line string) error { t.Logf("WATCH drain: %s", line); return nil }
	if len(observers) > 0 {
		observe = observers[0]
	}
	err := drainWatchFixture(p.lines, p.done, time.Now().Add(8*time.Second), observe, func() { p.finished = true })
	if !p.finished {
		t.Fatalf("WATCH shutdown deadline before reaping child: %v", err)
	}
	if err != nil || p.stderr.Len() != 0 {
		t.Fatalf("SIGINT drain: %v %s", err, p.stderr.String())
	}
}

// recordingProxy forwards real protocol traffic and records client input, so
// a successful SIGINT exit is checked together with an actual UNWATCH frame.
type recordingProxy struct {
	mu               sync.Mutex
	input            bytes.Buffer
	changed          chan struct{}
	closeConnections func()
}

func (p *recordingProxy) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n, err := p.input.Write(data)
	if p.changed != nil {
		close(p.changed)
	}
	p.changed = make(chan struct{})
	return n, err
}
func (p *recordingProxy) text() string { p.mu.Lock(); defer p.mu.Unlock(); return p.input.String() }
func (p *recordingProxy) acknowledged(revision uint64) (bool, <-chan struct{}, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.changed == nil {
		p.changed = make(chan struct{})
	}
	acks, err := fixtureWireACKs(p.input.String())
	if err != nil {
		return false, p.changed, err
	}
	for _, ack := range acks {
		if ack > revision {
			return false, p.changed, fmt.Errorf("ACK %d exceeds expected %d", ack, revision)
		}
		if ack == revision {
			return true, p.changed, nil
		}
	}
	return false, p.changed, nil
}

func watchProxy(t *testing.T, s *testServer) (string, *recordingProxy) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	// Both TLS hops verify the owned fixture certificate and endpoint identity.
	// Record only application bytes, after the inbound TLS handshake.
	var remoteTLS *tls.Config
	if s.scheme == "chunks" {
		cert, err := tls.LoadX509KeyPair(s.tlsCert, s.tlsKey)
		if err != nil {
			t.Fatal(err)
		}
		pem, err := os.ReadFile(s.tlsCert)
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			t.Fatal("invalid owned TLS certificate")
		}
		listener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{cert}})
		remoteTLS = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	}
	recorder := &recordingProxy{}
	var mu sync.Mutex
	var sockets []net.Conn
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			socket, err := listener.Accept()
			if err != nil {
				return
			}
			var remote net.Conn
			if remoteTLS != nil {
				remote, err = tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", s.address, remoteTLS)
			} else {
				remote, err = net.DialTimeout("tcp", s.address, 5*time.Second)
			}
			if err != nil {
				socket.Close()
				return
			}
			mu.Lock()
			sockets = append(sockets, socket, remote)
			mu.Unlock()
			if tlsSocket, ok := socket.(*tls.Conn); ok {
				if err := tlsSocket.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					socket.Close()
					remote.Close()
					return
				}
				if err := tlsSocket.Handshake(); err != nil {
					socket.Close()
					remote.Close()
					return
				}
				if err := tlsSocket.SetDeadline(time.Time{}); err != nil {
					socket.Close()
					remote.Close()
					return
				}
			}
			workers.Add(2)
			go func() { defer workers.Done(); _, _ = io.Copy(remote, io.TeeReader(socket, recorder)); remote.Close() }()
			go func() { defer workers.Done(); _, _ = io.Copy(socket, remote); socket.Close() }()
		}
	}()
	recorder.closeConnections = func() {
		mu.Lock()
		defer mu.Unlock()
		for _, socket := range sockets {
			socket.Close()
		}
	}
	t.Cleanup(func() {
		listener.Close()
		mu.Lock()
		for _, socket := range sockets {
			socket.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	uri, err := url.Parse(s.uri)
	if err != nil {
		t.Fatal(err)
	}
	uri.Host = listener.Addr().String()
	return uri.String(), recorder
}

func TestCLIWatchIntegration(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u64 REQUIRED, name text(16) NULL, flags bits(5), blob bytes(8)) CHUNK 2 x 2")
	uri, proxy := watchProxy(t, s)
	// Keep the feed alive across subprocesses so AFTER can replay retained data.
	keeper, err := connect(globalOptions{URI: s.uri, Timeout: 2 * time.Second}, console{})
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	if _, err = keeper.BeginWatch("WATCH world"); err != nil {
		t.Fatal(err)
	}
	p := startWatchProcess(t, uri, "--area", "0,0,0,0")
	trace := fixtureStart(t, p, false)
	position := strings.TrimPrefix(trace.raw[0], "start ")
	s.ok(t, "SET BLOCK 4 0 IN world id = 99")
	firstRevision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 18446744073709551615, name = 'door', flags = b'10110', blob = x'00ff'"))
	header, block := trace.waitMutation(t, p, "change", firstRevision, 1)
	if !strings.Contains(header, "user admin") || !strings.Contains(block, "block 0 0: (absent) -> {id = 18446744073709551615, name = 'door', flags = b'10110', blob = x'00ff'}") {
		t.Fatalf("%q %q", header, block)
	}
	secondRevision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world name = 'new'"))
	_, line := trace.waitMutation(t, p, "change", secondRevision, 1)
	if !strings.Contains(line, "name = 'door'") || !strings.Contains(line, "name = 'new'") {
		t.Fatal(line)
	}
	trace.stop(t, p, secondRevision, 1)
	if strings.Count(proxy.text(), "UNWATCH\r\n") != 1 {
		t.Fatal("missing UNWATCH")
	}
	resumed := startWatchProcess(t, s.uri, "--area", "0,0,0,0", "--after", position, "--json")
	replay := fixtureStart(t, resumed, true)
	// AFTER starts delivery at the requested retained frontier, even when
	// the producer's current completed frontier is newer.
	afterPosition, err := parseWatchPosition(position)
	if err != nil {
		t.Fatal(err)
	}
	if replay.epoch != afterPosition.Epoch {
		t.Fatalf("replay epoch changed: %v", replay.raw)
	}
	replay.frontier = afterPosition.Revision
	first, _ := replay.waitMutation(t, resumed, "change", firstRevision, 1)
	if !strings.Contains(first, `"id":"18446744073709551615"`) || !strings.Contains(first, `"name":"door"`) || strings.Contains(first, `"id":99`) {
		t.Fatal(first)
	}
	next, _ := replay.waitMutation(t, resumed, "change", secondRevision, 1)
	if !json.Valid([]byte(next)) || !strings.Contains(next, `"name":"new"`) {
		t.Fatal(next)
	}
	s.ok(t, "ALTER TABLE world ADD COLUMN light u4 DEFAULT 15")
	schemaRevision := fixtureTableFrontier(t, s)
	replay.waitSchema(t, resumed, schemaRevision, 2)
	thirdRevision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world light = 7"))
	next, _ = replay.waitMutation(t, resumed, "change", thirdRevision, 2)
	if !strings.Contains(next, `"light":15`) || !strings.Contains(next, `"light":7`) {
		t.Fatal(next)
	}
	replay.stop(t, resumed, thirdRevision, 2)
	stale := startWatchProcess(t, s.uri, "--after", watchEpoch+":0")
	staleTrace := fixtureStart(t, stale, false)
	if line := staleTrace.waitResync(t, stale, replay.epoch, thirdRevision); !strings.HasPrefix(line, "resync ") || !strings.Contains(line, "--after ") {
		t.Fatal(line)
	}
	staleTrace.stop(t, stale, thirdRevision, 2)
	code, _, errout := s.cli(t, "WATCH world\nquit\n", "shell")
	if code != 0 || !strings.Contains(errout, "chunk-cli watch <table>") {
		t.Fatalf("shell %d %q", code, errout)
	}
}

func TestCLIWatchTableDrop(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	p := startWatchProcess(t, s.uri)
	trace := fixtureStart(t, p, false)
	s.ok(t, "DROP TABLE world")
	trace.expectFailure(t, p, trace.frontier, "NO_TABLE")
}

func TestCLIWatchConnectionLoss(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	uri, proxy := watchProxy(t, s)
	p := startWatchProcess(t, uri, "--no-reconnect")
	trace := fixtureStart(t, p, false)
	proxy.closeConnections()
	trace.expectFailure(t, p, trace.frontier, "connection closed by the server")
}

func TestCLIWatchReconnectIntegration(t *testing.T) {
	for _, slot := range []bool{false, true} {
		t.Run(fmt.Sprint(slot), func(t *testing.T) {
			s := startServer(t, false)
			s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
			var args []string
			if slot {
				s.ok(t, "CREATE SLOT 'consumer' ON world")
				args = []string{"--slot", "consumer"}
			}
			keeper, err := connect(globalOptions{URI: s.uri, Timeout: 2 * time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			defer keeper.Close()
			if _, err := keeper.BeginWatch("WATCH world"); err != nil {
				t.Fatal(err)
			}
			uri, proxy := watchProxy(t, s)
			p := startWatchProcess(t, uri, args...)
			trace := fixtureStart(t, p, false)
			first := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
			trace.waitMutation(t, p, "change", first, 1)
			if slot {
				trace.awaitACK(t, proxy, p.lines, first, 1)
			}
			proxy.closeConnections()
			for {
				line := p.line(t)
				if strings.HasPrefix(line, "start ") {
					reconnected, err := newWatchFixture(line, false)
					if err != nil || reconnected.epoch != trace.epoch || reconnected.frontier != first {
						t.Fatalf("reconnect position %q: %v", line, err)
					}
					trace = reconnected
					break
				}
				if known, err := trace.ancillary(line, first, first, 1); !known || err != nil {
					t.Fatalf("before reconnect: %q: %v", line, err)
				}
			}
			second := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 9"))
			_, row := trace.waitMutation(t, p, "change", second, 1)
			if row != "  block 0 0: {id = 7} -> {id = 9}" {
				t.Fatal(row)
			}
			trace.stop(t, p, second, 1)
			want := fmt.Sprintf(" AFTER %s %d\r\n", trace.epoch, first)
			if !strings.Contains(proxy.text(), want) {
				t.Fatalf("missing resume frontier %q in %q", want, proxy.text())
			}
			if slot && slotAcked(t, s) != second {
				t.Fatal("slot did not acknowledge resumed output")
			}
		})
	}
}

func TestCLIWatchTLS(t *testing.T) {
	s := startServer(t, true)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	p := startWatchProcess(t, s.uri)
	trace := fixtureStart(t, p, false)
	revision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
	_, row := trace.waitMutation(t, p, "change", revision, 1)
	if row != "  block 0 0: (absent) -> {id = 7}" {
		t.Fatal(row)
	}
	trace.stop(t, p, revision, 1)
}

func TestCLIWatchUnavailableHistoricalSchema(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	keeper, err := connect(globalOptions{URI: s.uri, Timeout: 2 * time.Second}, console{})
	if err != nil {
		t.Fatal(err)
	}
	defer keeper.Close()
	start, err := keeper.BeginWatch("WATCH world")
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(start)
	revision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
	s.ok(t, "ALTER TABLE world ADD COLUMN name text(16) NULL")
	p := startWatchProcess(t, s.uri, "--after", words[1]+":"+words[2])
	trace := fixtureStart(t, p, false)
	trace.frontier = fixtureVersion(t, words[2])
	var validationError error
	err = drainWatchFixture(p.lines, p.done, time.Now().Add(8*time.Second), func(line string) error {
		trace.raw = append(trace.raw, line)
		if !strings.HasPrefix(line, "progress ") {
			validationError = fmt.Errorf("decoded with wrong historical schema: %s", line)
			return validationError
		}
		_, err := trace.ancillary(line, revision-1, revision-1, 1)
		if err != nil {
			validationError = err
		}
		return err
	}, func() { p.finished = true })
	t.Logf("Raw historical WATCH stream: %s", strings.Join(trace.raw, "\n"))
	if !p.finished {
		t.Fatalf("historical WATCH deadline before reaping child: %v", err)
	}
	if validationError != nil || err == nil || !strings.Contains(p.stderr.String(), "need schema version 1, DESCRIBE returned 2") {
		t.Fatalf("historical schema: %v; validation %v; %q", err, validationError, p.stderr.String())
	}
}

func TestCLIWatchSingleWorker(t *testing.T) {
	s := startServerConfigured(t, false, true, 1)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	p := startWatchProcess(t, s.uri)
	trace := fixtureStart(t, p, false)
	revision := fixtureVersion(t, s.ok(t, "SET BLOCK 0 0 IN world id = 7"))
	_, row := trace.waitMutation(t, p, "change", revision, 1)
	if row != "  block 0 0: (absent) -> {id = 7}" {
		t.Fatal(row)
	}
	trace.stop(t, p, revision, 1)
}
