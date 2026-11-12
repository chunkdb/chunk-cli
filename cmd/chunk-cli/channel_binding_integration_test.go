package main

import (
	"crypto/tls"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

const otherConnection = "AUTH_FAILED reason=channel_binding the login is bound to another TLS connection"

// startRelay ends the TLS connections it accepts with a certificate of its
// own and passes their bytes on over TLS connections it opens to target: what
// stands between a client that accepts a wrong certificate and the server.
// It returns the relay's address.
func startRelay(t *testing.T, target string) string {
	t.Helper()
	certPath, keyPath := writeTLSFixture(t)
	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load the relay certificate: %v", err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			near, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer near.Close()
				far, err := tls.Dial("tcp", target, &tls.Config{InsecureSkipVerify: true})
				if err != nil {
					return
				}
				defer far.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(far, near); done <- struct{}{} }()
				go func() { _, _ = io.Copy(near, far); done <- struct{}{} }()
				<-done
			}()
		}
	}()
	return listener.Addr().String()
}

// A login over TLS is bound to its connection: it passes a server that takes
// only bound logins, and does not pass a relay that ends TLS itself.
func TestCLIChannelBinding(t *testing.T) {
	t.Setenv(passwordEnv, "")
	strict := startServerConfigured(t, true, true, 4, "--auth-channel-binding", "require")
	if out := strict.ok(t, "PING"); out != "PONG\n" {
		t.Fatalf("bound login: %q", out)
	}
	if out := strict.ok(t, "--channel-binding", "require", "PING"); out != "PONG\n" {
		t.Fatalf("--channel-binding require: %q", out)
	}
	strict.fails(t, "AUTH_REQUIRED reason=channel_binding", "--channel-binding", "off", "PING")
	strict.fails(t, "channel binding is auto, off or require", "--channel-binding", "on", "PING")

	// Capped at TLS 1.2 the login binds to the server certificate.
	uri, err := chunkuri.Parse(strict.uri)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []chunkclient.ChannelBinding{chunkclient.ChannelBindingAuto, chunkclient.ChannelBindingOff} {
		client, err := chunkclient.Dial(chunkclient.Config{
			URI: uri, Timeout: 5 * time.Second, TLSInsecure: true, TLSMaxVersion: tls.VersionTLS12, ChannelBinding: mode,
		})
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_, err = client.Hello(chunkclient.Login{User: adminUser, Password: adminPassword})
		_ = client.Close()
		if mode == chunkclient.ChannelBindingAuto && err != nil {
			t.Fatalf("TLS 1.2 bound login: %v", err)
		}
		if mode == chunkclient.ChannelBindingOff && (err == nil || !strings.Contains(err.Error(), "AUTH_REQUIRED reason=channel_binding")) {
			t.Fatalf("TLS 1.2 unbound login: %v", err)
		}
	}

	// Through a relay the bound login fails and the unbound one passes.
	s := startServer(t, true)
	relayed := *s
	relayed.address = startRelay(t, s.address)
	relayed.uri = relayed.uriFor(adminUser, adminPassword)
	relayed.fails(t, otherConnection, "PING")
	relayed.fails(t, otherConnection, "--channel-binding", "require", "PING")
	if out := relayed.ok(t, "--channel-binding", "off", "PING"); out != "PONG\n" {
		t.Fatalf("unbound login through the relay: %q", out)
	}
	if out := s.ok(t, "PING"); out != "PONG\n" {
		t.Fatalf("bound login without the relay: %q", out)
	}

	// A user that takes only bound logins.
	if out := s.ok(t, "--new-password-file", writePasswordFile(t, "bot"), "CREATE USER bot PASSWORD REQUIRE CHANNEL BINDING"); out != "OK\n" {
		t.Fatalf("CREATE USER: %q", out)
	}
	if out := s.ok(t, "--json", "SHOW USERS"); !strings.Contains(out, `{"name":"bot","manages_users":false,"requires_channel_binding":true,"grants":{}}`) {
		t.Fatalf("SHOW USERS: %q", out)
	}
	bot := func(args ...string) (int, string, string) {
		t.Helper()
		return s.cliAt(t, s.uriFor("bot", "bot"), "", args...)
	}
	if code, out, errOut := bot("PING"); code != 0 || out != "PONG\n" {
		t.Fatalf("bot bound: %d %q %q", code, out, errOut)
	}
	if code, _, errOut := bot("--channel-binding", "off", "PING"); code != 1 || !strings.Contains(errOut, "AUTH_FAILED reason=auth_failed") {
		t.Fatalf("bot unbound: %d %q", code, errOut)
	}
	s.ok(t, "ALTER USER bot NO REQUIRE CHANNEL BINDING")
	if code, out, errOut := bot("--channel-binding", "off", "PING"); code != 0 || out != "PONG\n" {
		t.Fatalf("bot unbound after NO REQUIRE: %d %q %q", code, out, errOut)
	}

	// Without TLS there is nothing to bind to.
	plain := startServer(t, false)
	if out := plain.ok(t, "PING"); out != "PONG\n" {
		t.Fatalf("plain auto: %q", out)
	}
	plain.fails(t, "channel binding is required, and this connection is not TLS", "--channel-binding", "require", "PING")

	// Without a user there is no login to bind.
	open := startServerWith(t, true, false)
	if out := open.ok(t, "PING"); out != "PONG\n" {
		t.Fatalf("--auth none over TLS: %q", out)
	}
	open.fails(t, "channel binding is required, and there is no user to log in as", "--channel-binding", "require", "PING")
}
