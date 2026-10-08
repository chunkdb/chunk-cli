package chunkclient

import (
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// MinIterations is the fewest PBKDF2 iterations the server takes in a
// verifier, and the fewest a login accepts from the server.
const MinIterations = 4096

// scramChannelBinding is the GS2 header "n,," (no channel binding) in base64.
const scramChannelBinding = "c=biws"

// scramLogin is one SCRAM-SHA-256 login (RFC 5802, RFC 7677) in progress.
type scramLogin struct {
	user     string
	password string
	nonce    string
	// first is the client-first message, firstBare it without the GS2
	// header.
	first, firstBare string
}

// startLogin returns the login with its client-first message; the nonce is
// 18 random bytes in base64.
func startLogin(user, password string) (*scramLogin, error) {
	if err := checkUserName(user); err != nil {
		return nil, err
	}
	random := make([]byte, 18)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("read random nonce: %w", err)
	}
	return newLogin(user, password, base64.StdEncoding.EncodeToString(random)), nil
}

func newLogin(user, password, nonce string) *scramLogin {
	bare := "n=" + user + ",r=" + nonce
	return &scramLogin{user: user, password: password, nonce: nonce, first: "n,," + bare, firstBare: bare}
}

// checkUserName refuses a name that HELLO 3 USER and the client-first
// message cannot carry as it is.
func checkUserName(user string) error {
	if user == "" {
		return errors.New("empty user name")
	}
	if strings.ContainsFunc(user, func(r rune) bool { return r <= ' ' || r == 0x7f || r == ',' || r == '=' }) {
		return fmt.Errorf("the user name %q must not contain spaces, control characters, ',' or '='", user)
	}
	return nil
}

// finish takes the server-first message and returns the client-final message
// and the server signature the server must answer with.
func (l *scramLogin) finish(serverFirst string) (clientFinal, serverSignature string, err error) {
	nonce, salt, iterations, err := parseServerFirst(serverFirst)
	if err != nil {
		return "", "", err
	}
	if !strings.HasPrefix(nonce, l.nonce) || len(nonce) == len(l.nonce) {
		return "", "", errors.New("the server's SCRAM nonce does not continue the client nonce")
	}
	salted, err := pbkdf2.Key(sha256.New, l.password, salt, iterations, sha256.Size)
	if err != nil {
		return "", "", fmt.Errorf("derive the SCRAM key: %w", err)
	}
	clientKey := hmacSHA256(salted, []byte("Client Key"))
	storedKey := sha256.Sum256(clientKey)
	serverKey := hmacSHA256(salted, []byte("Server Key"))

	withoutProof := scramChannelBinding + ",r=" + nonce
	authMessage := []byte(l.firstBare + "," + serverFirst + "," + withoutProof)
	signature := hmacSHA256(storedKey[:], authMessage)
	proof := make([]byte, len(clientKey))
	for i := range proof {
		proof[i] = clientKey[i] ^ signature[i]
	}
	clientFinal = withoutProof + ",p=" + base64.StdEncoding.EncodeToString(proof)
	serverSignature = "v=" + base64.StdEncoding.EncodeToString(hmacSHA256(serverKey, authMessage))
	return clientFinal, serverSignature, nil
}

// parseServerFirst reads `r=<nonce>,s=<salt>,i=<iterations>`.
func parseServerFirst(message string) (nonce string, salt []byte, iterations int, err error) {
	malformed := fmt.Errorf("not a SCRAM server-first message: %q", message)
	parts := strings.Split(message, ",")
	if len(parts) != 3 {
		return "", nil, 0, malformed
	}
	nonce, okNonce := strings.CutPrefix(parts[0], "r=")
	saltText, okSalt := strings.CutPrefix(parts[1], "s=")
	iterationsText, okIterations := strings.CutPrefix(parts[2], "i=")
	if !okNonce || !okSalt || !okIterations || nonce == "" {
		return "", nil, 0, malformed
	}
	salt, err = base64.StdEncoding.DecodeString(saltText)
	if err != nil || len(salt) == 0 {
		return "", nil, 0, malformed
	}
	iterations, err = strconv.Atoi(iterationsText)
	if err != nil || iterations <= 0 {
		return "", nil, 0, malformed
	}
	if iterations < MinIterations {
		return "", nil, 0, fmt.Errorf("the server asks for %d SCRAM iterations, fewer than %d", iterations, MinIterations)
	}
	return nonce, salt, iterations, nil
}

// Verifier computes the SCRAM verifier of a password with a random 16-byte
// salt, in the form CREATE USER and ALTER USER take:
// `SCRAM-SHA-256$<iterations>:<salt>$<StoredKey>:<ServerKey>` (base64 parts).
// The password itself is never sent.
func Verifier(password string, iterations int) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read random salt: %w", err)
	}
	return verifierWithSalt(password, salt, iterations)
}

func verifierWithSalt(password string, salt []byte, iterations int) (string, error) {
	if iterations < MinIterations {
		return "", fmt.Errorf("a verifier needs at least %d iterations, got %d", MinIterations, iterations)
	}
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("derive the SCRAM key: %w", err)
	}
	storedKey := sha256.Sum256(hmacSHA256(salted, []byte("Client Key")))
	serverKey := hmacSHA256(salted, []byte("Server Key"))
	encode := base64.StdEncoding.EncodeToString
	return "SCRAM-SHA-256$" + strconv.Itoa(iterations) + ":" + encode(salt) + "$" + encode(storedKey[:]) + ":" + encode(serverKey), nil
}

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return mac.Sum(nil)
}
