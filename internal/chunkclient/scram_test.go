package chunkclient

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The SCRAM-SHA-256 example of RFC 7677, section 3.
const (
	rfcNonce       = "rOprNGfwEbeRWgbNEkqO"
	rfcServerFirst = "r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096"
)

func TestScramRFC7677(t *testing.T) {
	login := newLogin("user", "pencil", rfcNonce)
	if login.first != "n,,n=user,r="+rfcNonce {
		t.Fatalf("client-first %q", login.first)
	}
	clientFinal, signature, err := login.finish(rfcServerFirst)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if want := "c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0,p=dHzbZapWIk4jUhN+Ute9ytag9zjfMHgsqmmiz7AndVQ="; clientFinal != want {
		t.Fatalf("client-final %q, want %q", clientFinal, want)
	}
	if want := "v=6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4="; signature != want {
		t.Fatalf("server signature %q, want %q", signature, want)
	}
}

func TestScramRefusesServerFirst(t *testing.T) {
	login := newLogin("user", "pencil", rfcNonce)
	for message, want := range map[string]string{
		"r=other,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096":            "does not continue",
		"r=" + rfcNonce + ",s=W22ZaJ0SNY7soEsUEjb6gQ==,i=4096": "does not continue",
		"r=" + rfcNonce + "x,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=1":   "fewer than 4096",
		"r=" + rfcNonce + "x,s=W22ZaJ0SNY7soEsUEjb6gQ==,i=x":   "not a SCRAM server-first",
		"r=" + rfcNonce + "x,s=!!,i=4096":                      "not a SCRAM server-first",
		"r=" + rfcNonce + "x,i=4096":                           "not a SCRAM server-first",
		"e=other-error":                                        "not a SCRAM server-first",
	} {
		if _, _, err := login.finish(message); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got %v, want %q", message, err, want)
		}
	}
}

func TestVerifier(t *testing.T) {
	format := regexp.MustCompile(`^SCRAM-SHA-256\$4096:([A-Za-z0-9+/=]+)\$([A-Za-z0-9+/=]+):([A-Za-z0-9+/=]+)$`)
	first, err := Verifier("pencil", MinIterations)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	parts := format.FindStringSubmatch(first)
	if parts == nil {
		t.Fatalf("verifier %q", first)
	}
	for i, size := range []int{16, 32, 32} {
		if decoded, err := base64.StdEncoding.DecodeString(parts[i+1]); err != nil || len(decoded) != size {
			t.Fatalf("verifier part %d of %q: %d bytes, %v", i+1, first, len(decoded), err)
		}
	}
	if second, _ := Verifier("pencil", MinIterations); second == first {
		t.Fatal("two verifiers share a salt")
	}
	if more, err := Verifier("pencil", 5000); err != nil || !strings.HasPrefix(more, "SCRAM-SHA-256$5000:") {
		t.Fatalf("5000 iterations: %q, %v", more, err)
	}
	if _, err := Verifier("pencil", 4095); err == nil {
		t.Fatal("expected an error for 4095 iterations")
	}

	// The keys of the RFC 7677 salt: the server proves the RFC's signature
	// with them.
	salt, _ := base64.StdEncoding.DecodeString("W22ZaJ0SNY7soEsUEjb6gQ==")
	verifier, err := verifierWithSalt("pencil", salt, 4096)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	server := parseTestVerifier(t, verifier)
	authMessage := "n=user,r=" + rfcNonce + "," + rfcServerFirst + ",c=biws,r=rOprNGfwEbeRWgbNEkqO%hvYDpWUa2RaTCAfuxFIlj)hNlF$k0"
	if got := base64.StdEncoding.EncodeToString(hmacSHA256(server.serverKey, []byte(authMessage))); got != "6rriTRBi23WpRR/wtup+mMhUZUn/dB5nLTJRsjl95G4=" {
		t.Fatalf("server signature from the verifier: %s", got)
	}
}

type testVerifier struct {
	iterations           int
	salt                 []byte
	storedKey, serverKey []byte
}

func parseTestVerifier(t *testing.T, text string) testVerifier {
	t.Helper()
	var v testVerifier
	rest, ok := strings.CutPrefix(text, "SCRAM-SHA-256$")
	head, keys, ok2 := strings.Cut(rest, "$")
	iterations, saltText, ok3 := strings.Cut(head, ":")
	stored, server, ok4 := strings.Cut(keys, ":")
	if !ok || !ok2 || !ok3 || !ok4 {
		t.Fatalf("verifier %q", text)
	}
	var err error
	v.iterations, err = strconv.Atoi(iterations)
	if err != nil {
		t.Fatalf("verifier %q: %v", text, err)
	}
	v.salt, _ = base64.StdEncoding.DecodeString(saltText)
	v.storedKey, _ = base64.StdEncoding.DecodeString(stored)
	v.serverKey, _ = base64.StdEncoding.DecodeString(server)
	return v
}

// readFrame reads a request line and its one parameter frame.
func readFrame(r *bufio.Reader) (line, frame string, err error) {
	line, err = r.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	header, err := r.ReadString('\n')
	if err != nil {
		return "", "", err
	}
	length, err := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(header, "$"), "\r\n"))
	if err != nil {
		return "", "", err
	}
	data := make([]byte, length+2)
	if _, err := io.ReadFull(r, data); err != nil {
		return "", "", err
	}
	return line, string(data[:length]), nil
}

// fakeLoginServer runs the server side of a SCRAM login over conn with the
// verifier; badSignature answers with another server's signature.
func fakeLoginServer(conn net.Conn, verifier testVerifier, badSignature bool) <-chan error {
	done := make(chan error, 1)
	go func() {
		defer conn.Close()
		done <- func() error {
			r := bufio.NewReader(conn)
			line, first, err := readFrame(r)
			if err != nil {
				return err
			}
			bare, ok := strings.CutPrefix(first, "n,,")
			_, clientNonce, ok2 := strings.Cut(bare, ",r=")
			if !ok || !ok2 || line != "HELLO 3 USER bot $1\r\n" || !strings.HasPrefix(bare, "n=bot,") {
				return fmt.Errorf("HELLO %q %q", line, first)
			}
			serverFirst := fmt.Sprintf("r=%sSERVER,s=%s,i=%d", clientNonce, base64.StdEncoding.EncodeToString(verifier.salt), verifier.iterations)
			if _, err := io.WriteString(conn, "+SCRAM "+serverFirst+"\r\n"); err != nil {
				return err
			}
			line, final, err := readFrame(r)
			if err != nil {
				return err
			}
			withoutProof, proofText, ok := strings.Cut(final, ",p=")
			if line != "AUTH $1\r\n" || !ok || withoutProof != "c=biws,r="+clientNonce+"SERVER" {
				return fmt.Errorf("AUTH %q %q", line, final)
			}
			authMessage := []byte(bare + "," + serverFirst + "," + withoutProof)
			proof, _ := base64.StdEncoding.DecodeString(proofText)
			clientSignature := hmacSHA256(verifier.storedKey, authMessage)
			clientKey := make([]byte, len(clientSignature))
			for i := range clientKey {
				clientKey[i] = proof[i] ^ clientSignature[i]
			}
			if sum := sha256.Sum256(clientKey); !bytes.Equal(sum[:], verifier.storedKey) {
				_, err := io.WriteString(conn, "-ERR AUTH_FAILED invalid user or password\r\n")
				return err
			}
			key := verifier.serverKey
			if badSignature {
				key = []byte("another server")
			}
			_, err = io.WriteString(conn, helloReplySigned("v="+base64.StdEncoding.EncodeToString(hmacSHA256(key, authMessage))))
			return err
		}()
	}()
	return done
}

func TestLogin(t *testing.T) {
	text, err := Verifier("s3cret:/@", MinIterations)
	if err != nil {
		t.Fatalf("verifier: %v", err)
	}
	verifier := parseTestVerifier(t, text)
	cases := []struct {
		name, password string
		badSignature   bool
		want           string
		is             error
	}{
		{"login", "s3cret:/@", false, "", nil},
		{"wrong password", "wrong", false, "AUTH_FAILED", ErrAuthFailed},
		{"wrong server signature", "s3cret:/@", true, "could not prove it knows the password", nil},
	}
	for _, tc := range cases {
		clientConn, serverConn := net.Pipe()
		done := fakeLoginServer(serverConn, verifier, tc.badSignature)
		client := &Client{conn: clientConn, reader: bufio.NewReader(clientConn), writer: bufio.NewWriter(clientConn), timeout: 5 * time.Second}
		info, err := client.Hello(Login{User: "bot", Password: tc.password})
		_ = client.Close()
		if serverErr := <-done; serverErr != nil && !errors.Is(serverErr, io.ErrClosedPipe) {
			t.Fatalf("%s: server: %v", tc.name, serverErr)
		}
		if tc.want == "" {
			if err != nil || !strings.HasPrefix(info.ServerSignature, "v=") || client.Info() != info {
				t.Fatalf("%s: %+v, %v", tc.name, info, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) || (tc.is != nil && !errors.Is(err, tc.is)) {
			t.Fatalf("%s: got %v, want %q", tc.name, err, tc.want)
		}
		if client.Info() != nil {
			t.Fatalf("%s: the failed login kept the server info", tc.name)
		}
		if tc.badSignature && client.Broken() == nil {
			t.Fatalf("%s: the connection is still usable", tc.name)
		}
	}
}

func TestServerErrorIs(t *testing.T) {
	denied := error(&ServerError{Code: "PERMISSION_DENIED", Message: "WRITE on world"})
	if !errors.Is(fmt.Errorf("wrapped: %w", denied), ErrPermissionDenied) || errors.Is(denied, ErrAuthFailed) {
		t.Fatal("PERMISSION_DENIED does not match its code error alone")
	}
	if errors.Is(denied, &ServerError{Code: "PERMISSION_DENIED", Message: "READ on x"}) {
		t.Fatal("an error with a message matched")
	}
}
