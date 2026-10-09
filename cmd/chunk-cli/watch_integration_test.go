package main

import (
	"bufio"
	"bytes"
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
	t.Helper()
	p := &watchProcess{lines: make(chan string, 64), done: make(chan error, 1)}
	all := append([]string{"-test.run=^TestWatchProcess$", "--", "--uri", uri, "--tls-insecure", "watch", "world"}, args...)
	p.command = exec.Command(os.Args[0], all...)
	p.command.Env = append(os.Environ(), "CHUNKCLI_WATCH_PROCESS=1")
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
		if !p.finished {
			_ = p.command.Process.Signal(os.Interrupt)
			select {
			case <-p.done:
			case <-time.After(5 * time.Second):
				_ = p.command.Process.Kill()
				<-p.done
			}
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
func (p *watchProcess) interrupt(t *testing.T) {
	t.Helper()
	if err := p.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-p.done:
		p.finished = true
		if err != nil || p.stderr.Len() != 0 {
			t.Fatalf("SIGINT exit: %v %s", err, p.stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("watch did not stop on SIGINT")
	}
}

// recordingProxy forwards real protocol traffic and records client input, so
// a successful SIGINT exit is checked together with an actual UNWATCH frame.
type recordingProxy struct {
	mu               sync.Mutex
	input            bytes.Buffer
	closeConnections func()
}

func (p *recordingProxy) Write(data []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.input.Write(data)
}
func (p *recordingProxy) text() string { p.mu.Lock(); defer p.mu.Unlock(); return p.input.String() }
func watchProxy(t *testing.T, s *testServer) (string, *recordingProxy) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
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
			remote, err := net.Dial("tcp", s.address)
			if err != nil {
				socket.Close()
				return
			}
			mu.Lock()
			sockets = append(sockets, socket, remote)
			mu.Unlock()
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
	start := p.line(t)
	if !strings.HasPrefix(start, "start ") {
		t.Fatal(start)
	}
	position := strings.TrimPrefix(start, "start ")
	s.ok(t, "SET BLOCK 4 0 IN world id = 99")
	s.ok(t, "SET BLOCK 0 0 IN world id = 18446744073709551615, name = 'door', flags = b'10110', blob = x'00ff'")
	header, block := p.line(t), p.line(t)
	t.Logf("Observed output:\n%s\n%s\n%s", start, header, block)
	if !strings.Contains(header, "user admin") || !strings.Contains(block, "block 0 0: (absent) -> {id = 18446744073709551615, name = 'door', flags = b'10110', blob = x'00ff'}") {
		t.Fatalf("%q %q", header, block)
	}
	s.ok(t, "SET BLOCK 0 0 IN world name = 'new'")
	_ = p.line(t)
	if line := p.line(t); !strings.Contains(line, "name = 'door'") || !strings.Contains(line, "name = 'new'") {
		t.Fatal(line)
	}
	p.interrupt(t)
	if strings.Count(proxy.text(), "UNWATCH\r\n") != 1 {
		t.Fatal("missing UNWATCH")
	}
	resumed := startWatchProcess(t, s.uri, "--area", "0,0,0,0", "--after", position, "--json")
	var startJSON map[string]json.RawMessage
	if err := json.Unmarshal([]byte(resumed.line(t)), &startJSON); err != nil || string(startJSON["type"]) != `"start"` {
		t.Fatalf("start JSON %+v %v", startJSON, err)
	}
	first := resumed.line(t)
	if !strings.Contains(first, `"type":"change"`) || !strings.Contains(first, `"id":18446744073709551615`) || !strings.Contains(first, `"name":"door"`) || strings.Contains(first, `"id":99`) {
		t.Fatal(first)
	}
	if line := resumed.line(t); !json.Valid([]byte(line)) || !strings.Contains(line, `"name":"new"`) {
		t.Fatal(line)
	}
	s.ok(t, "ALTER TABLE world ADD COLUMN light u4 DEFAULT 15")
	if line := resumed.line(t); !strings.Contains(line, `"type":"schema"`) {
		t.Fatal(line)
	}
	s.ok(t, "SET BLOCK 0 0 IN world light = 7")
	if line := resumed.line(t); !strings.Contains(line, `"light":15`) || !strings.Contains(line, `"light":7`) {
		t.Fatal(line)
	}
	resumed.interrupt(t)
	stale := startWatchProcess(t, s.uri, "--after", watchEpoch+":0")
	_ = stale.line(t)
	if line := stale.line(t); !strings.HasPrefix(line, "resync ") || !strings.Contains(line, "--after ") {
		t.Fatal(line)
	}
	stale.interrupt(t)
	code, _, errout := s.cli(t, "WATCH world\nquit\n", "shell")
	if code != 0 || !strings.Contains(errout, "chunk-cli watch <table>") {
		t.Fatalf("shell %d %q", code, errout)
	}
}

func TestCLIWatchTableDrop(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	p := startWatchProcess(t, s.uri)
	_ = p.line(t)
	s.ok(t, "DROP TABLE world")
	select {
	case err := <-p.done:
		p.finished = true
		if err == nil || !strings.Contains(p.stderr.String(), "NO_TABLE") {
			t.Fatalf("connection end: %v %q", err, p.stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal(fmt.Errorf("watch did not end after DROP TABLE"))
	}
}

func TestCLIWatchConnectionLoss(t *testing.T) {
	s := startServer(t, false)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	uri, proxy := watchProxy(t, s)
	p := startWatchProcess(t, uri)
	_ = p.line(t)
	proxy.closeConnections()
	select {
	case err := <-p.done:
		p.finished = true
		if err == nil || !strings.Contains(p.stderr.String(), "connection closed by the server") {
			t.Fatalf("connection loss: %v %q", err, p.stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("watch did not end after connection loss")
	}
}

func TestCLIWatchTLS(t *testing.T) {
	s := startServer(t, true)
	s.ok(t, "CREATE TABLE world (id u8) CHUNK 2 x 2")
	p := startWatchProcess(t, s.uri)
	if line := p.line(t); !strings.HasPrefix(line, "start ") {
		t.Fatal(line)
	}
	s.ok(t, "SET BLOCK 0 0 IN world id = 7")
	_ = p.line(t)
	if line := p.line(t); !strings.Contains(line, "id = 7") {
		t.Fatal(line)
	}
	p.interrupt(t)
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
	s.ok(t, "SET BLOCK 0 0 IN world id = 7")
	s.ok(t, "ALTER TABLE world ADD COLUMN name text(16) NULL")
	p := startWatchProcess(t, s.uri, "--after", words[1]+":"+words[2])
	_ = p.line(t)
	select {
	case err := <-p.done:
		p.finished = true
		if err == nil || !strings.Contains(p.stderr.String(), "need schema version 1, DESCRIBE returned 2") {
			t.Fatalf("historical schema: %v %q", err, p.stderr.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatal("watch did not reject unavailable historical schema")
	}
	for line := range p.lines {
		t.Fatalf("decoded with the wrong schema: %q", line)
	}
}
