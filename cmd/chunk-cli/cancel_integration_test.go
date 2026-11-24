//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestCLITLSCertificateAuthority(t *testing.T) {
	secure := startServer(t, true)
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--uri", secure.uri, "--tls-ca", secure.tlsCert, "PING"}, strings.NewReader(""), &stdout, &stderr); code != 0 || stdout.String() != "PONG\n" {
		t.Fatalf("--tls-ca: %d %q %q", code, stdout.String(), stderr.String())
	}
	empty := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(empty, []byte("no certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--uri", secure.uri, "--tls-ca", empty, "PING"}, strings.NewReader(""), &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "no PEM certificate") {
		t.Fatalf("an empty CA file: %d %q", code, stderr.String())
	}
}

func TestCLICancelOnInterrupt(t *testing.T) {
	s := startServer(t, false)
	opts := globalOptions{URI: s.uri, Timeout: 2 * time.Second}
	client, reconnect, err := connectAgain(opts, console{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	sent := make(chan *chunkclient.Client, 1)
	var stderr lockedBuffer
	stop := cancelOnInterrupt(client, func() (*chunkclient.Client, error) {
		other, err := reconnect()
		sent <- other
		return other, err
	}, &stderr)
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case other := <-sent:
		if other == nil || other.Info().ConnectionID == client.Info().ConnectionID {
			t.Fatal("CANCEL did not use a second connection")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl-C did not start a CANCEL")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(stderr.String(), "could no longer be cancelled") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(stderr.String(), "cancelling the statement") || !strings.Contains(stderr.String(), "could no longer be cancelled") {
		t.Fatalf("%q", stderr.String())
	}
	if _, err := client.Do("PING"); err != nil {
		t.Fatalf("the connection is not usable after CANCEL: %v", err)
	}
}
