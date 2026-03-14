package main

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

type shellServerState struct {
	mu       sync.Mutex
	sawAuth  bool
	commands []string
	blocks   map[string]string
}

func (s *shellServerState) record(cmd string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands = append(s.commands, cmd)
}

func (s *shellServerState) setAuth() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sawAuth = true
}

func (s *shellServerState) setBlock(x string, y string, bits string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blocks[x+":"+y] = bits
}

func (s *shellServerState) getBlock(x string, y string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bits, ok := s.blocks[x+":"+y]; ok {
		return bits
	}
	return "0000"
}

func startShellTestServer(t *testing.T, token string) (string, *shellServerState, func()) {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	state := &shellServerState{blocks: make(map[string]string)}
	done := make(chan struct{})

	go func() {
		defer close(done)

		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		reader := bufio.NewReader(conn)
		writer := bufio.NewWriter(conn)
		authed := token == ""

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}

			line = strings.TrimRight(line, "\r\n")
			if strings.TrimSpace(line) == "" {
				continue
			}

			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}

			cmd := strings.ToUpper(fields[0])
			state.record(cmd)

			switch cmd {
			case "PING":
				if err := writeSimple(writer, "PONG"); err != nil {
					return
				}
			case "AUTH":
				if len(fields) != 2 {
					if err := writeError(writer, "INVALID_ARGUMENT AUTH requires token"); err != nil {
						return
					}
					continue
				}
				if fields[1] != token {
					if err := writeError(writer, "AUTH_FAILED invalid token"); err != nil {
						return
					}
					continue
				}
				authed = true
				state.setAuth()
				if err := writeSimple(writer, "OK"); err != nil {
					return
				}
			case "SET":
				if !authed {
					if err := writeError(writer, "AUTH_REQUIRED use AUTH <token>"); err != nil {
						return
					}
					continue
				}
				if len(fields) != 4 {
					if err := writeError(writer, "INVALID_ARGUMENT SET requires 3 args"); err != nil {
						return
					}
					continue
				}
				if !isBits(fields[3]) {
					if err := writeError(writer, "INVALID_ARGUMENT invalid bits"); err != nil {
						return
					}
					continue
				}
				state.setBlock(fields[1], fields[2], fields[3])
				if err := writeSimple(writer, "OK"); err != nil {
					return
				}
			case "GET":
				if !authed {
					if err := writeError(writer, "AUTH_REQUIRED use AUTH <token>"); err != nil {
						return
					}
					continue
				}
				if len(fields) != 3 {
					if err := writeError(writer, "INVALID_ARGUMENT GET requires 2 args"); err != nil {
						return
					}
					continue
				}
				if err := writeBulk(writer, []byte(state.getBlock(fields[1], fields[2]))); err != nil {
					return
				}
			case "INFO":
				if !authed {
					if err := writeError(writer, "AUTH_REQUIRED use AUTH <token>"); err != nil {
						return
					}
					continue
				}
				if err := writeBulk(writer, []byte("chunkdb_version=1\n")); err != nil {
					return
				}
			case "CHUNK":
				if err := writeBulk(writer, []byte("0000")); err != nil {
					return
				}
			case "CHUNKBIN":
				if err := writeBulk(writer, []byte{0xAA, 0x55}); err != nil {
					return
				}
			case "QUIT":
				_ = writeSimple(writer, "BYE")
				return
			default:
				if err := writeError(writer, "UNKNOWN_COMMAND "+fields[0]); err != nil {
					return
				}
			}
		}
	}()

	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	uri := fmt.Sprintf("chunk://%s@%s:%d/", token, host, port)
	stop := func() {
		_ = ln.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("server did not stop in time")
		}
	}

	return uri, state, stop
}

func writeSimple(w *bufio.Writer, payload string) error {
	if _, err := w.WriteString("+" + payload + "\r\n"); err != nil {
		return err
	}
	return w.Flush()
}

func writeError(w *bufio.Writer, message string) error {
	if _, err := w.WriteString("-ERR " + message + "\r\n"); err != nil {
		return err
	}
	return w.Flush()
}

func writeBulk(w *bufio.Writer, payload []byte) error {
	if _, err := fmt.Fprintf(w, "$%d\r\n", len(payload)); err != nil {
		return err
	}
	if _, err := w.Write(payload); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n"); err != nil {
		return err
	}
	return w.Flush()
}

func isBits(bits string) bool {
	for _, ch := range bits {
		if ch != '0' && ch != '1' {
			return false
		}
	}
	return bits != ""
}

func TestRunShellConnectAuthPingGetSetQuit(t *testing.T) {
	uri, state, stop := startShellTestServer(t, "dev-token")
	defer stop()

	parsed, err := chunkuri.Parse(uri)
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}

	client, err := chunkclient.Dial(chunkclient.Config{URI: parsed, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	input := strings.NewReader("ping\nset 1 2 1010\nget 1 2\nquit\n")
	var out bytes.Buffer
	var errOut bytes.Buffer

	if err := runShell(client, parsed.Token, input, &out, &errOut); err != nil {
		t.Fatalf("run shell: %v", err)
	}

	if errOut.Len() != 0 {
		t.Fatalf("expected empty stderr, got %q", errOut.String())
	}

	state.mu.Lock()
	sawAuth := state.sawAuth
	state.mu.Unlock()
	if !sawAuth {
		t.Fatalf("expected shell auto-auth to run AUTH")
	}

	output := out.String()
	if strings.Count(output, "chunk> ") < 4 {
		t.Fatalf("expected repeated prompt, got %q", output)
	}
	for _, expected := range []string{"PONG", "OK", "1010", "BYE"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("expected %q in output %q", expected, output)
		}
	}
}

func TestRunShellExitAlias(t *testing.T) {
	uri, _, stop := startShellTestServer(t, "")
	defer stop()

	parsed, err := chunkuri.Parse(uri)
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}

	client, err := chunkclient.Dial(chunkclient.Config{URI: parsed, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	var out bytes.Buffer
	var errOut bytes.Buffer
	if err := runShell(client, "", strings.NewReader("exit\n"), &out, &errOut); err != nil {
		t.Fatalf("run shell: %v", err)
	}

	if out.String() != "chunk> " {
		t.Fatalf("unexpected output %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("expected no stderr output, got %q", errOut.String())
	}
}
