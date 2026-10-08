package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

// passwordEnv names the environment variable with the login password.
const passwordEnv = "CHUNKDB_PASSWORD"

// console is where passwords are asked for: stdin when it is a terminal,
// with the prompts on stderr.
type console struct {
	in  io.Reader
	out io.Writer
}

// terminal returns stdin when it is a terminal a password can be read from
// without echo.
func (c console) terminal() (*os.File, bool) {
	f, ok := c.in.(*os.File)
	return f, ok && isTerminal(f)
}

func (c console) askPassword(prompt string) (string, error) {
	f, ok := c.terminal()
	if !ok {
		return "", errors.New("stdin is not a terminal to ask for the password on")
	}
	password, err := readPasswordNoEcho(f, prompt, c.out)
	if err != nil {
		return "", err
	}
	if password == "" {
		return "", errors.New("empty password")
	}
	return password, nil
}

// loginFor returns the user and password to log in with. The user is
// --user or the URI's; the password comes from --password-file, the URI,
// CHUNKDB_PASSWORD, or is asked for when stdin is a terminal. Without a user
// the connection logs in without one.
func loginFor(opts globalOptions, uri chunkuri.Parsed, term console) (chunkclient.Login, error) {
	user := uri.User
	if opts.User != "" {
		user = opts.User
	}
	if user == "" {
		if opts.PasswordFile != "" {
			return chunkclient.Login{}, errors.New("--password-file needs a user (--user or chunk://user@host/)")
		}
		return chunkclient.Login{}, nil
	}
	login := chunkclient.Login{User: user}
	switch {
	case opts.PasswordFile != "":
		password, err := readPasswordFile(opts.PasswordFile)
		if err != nil {
			return chunkclient.Login{}, err
		}
		login.Password = password
	case uri.Password != "" && uri.User == user:
		login.Password = uri.Password
	case os.Getenv(passwordEnv) != "":
		login.Password = os.Getenv(passwordEnv)
	default:
		if _, ok := term.terminal(); !ok {
			return chunkclient.Login{}, fmt.Errorf("no password for user %s: give --password-file, %s or chunk://user:password@host/", user, passwordEnv)
		}
		password, err := term.askPassword("Password for " + user + ": ")
		if err != nil {
			return chunkclient.Login{}, err
		}
		login.Password = password
	}
	return login, nil
}

// readPasswordFile returns the first line of the file, without its line end.
func readPasswordFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("read the password: %w", err)
	}
	defer f.Close()
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password from %s: %w", path, err)
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return "", fmt.Errorf("the password file %s is empty", path)
	}
	return line, nil
}

// passwordStatement rewrites `CREATE USER <name> PASSWORD [MANAGES USERS]`
// and `ALTER USER <name> PASSWORD` into the VERIFIER statements the server
// takes, with the verifier as $1; ok is false for any other statement.
func passwordStatement(statement string) (rewritten, user string, ok bool) {
	words := strings.Fields(statement)
	if len(words) < 4 || !strings.EqualFold(words[1], "USER") || !strings.EqualFold(words[3], "PASSWORD") ||
		(!strings.EqualFold(words[0], "CREATE") && !strings.EqualFold(words[0], "ALTER")) {
		return "", "", false
	}
	words[3] = "VERIFIER $1"
	return strings.Join(words, " "), words[2], true
}

// newPassword returns the password of a PASSWORD statement: the first line of
// --new-password-file, or asked for twice on the terminal.
func newPassword(opts statementOptions, user string, term console) (string, error) {
	if opts.newPasswordFile != "" {
		return readPasswordFile(opts.newPasswordFile)
	}
	if _, ok := term.terminal(); !ok {
		return "", errors.New("PASSWORD needs --new-password-file <file>, or a terminal to ask for the password on")
	}
	password, err := term.askPassword("New password for " + user + ": ")
	if err != nil {
		return "", err
	}
	again, err := term.askPassword("Repeat the password: ")
	if err != nil {
		return "", err
	}
	if again != password {
		return "", errors.New("the passwords do not match")
	}
	return password, nil
}
