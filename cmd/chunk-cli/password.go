package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
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
	// HELLO carries the name unquoted, so a name outside the grammar would
	// change the statement.
	if !validUserName(user) {
		return chunkclient.Login{}, fmt.Errorf("invalid user name %q: names are [a-z_][a-z0-9_]*", user)
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

func validUserName(name string) bool {
	return name != "" && (name[0] < '0' || name[0] > '9') && strings.Trim(name, "abcdefghijklmnopqrstuvwxyz0123456789_") == ""
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

// passwordStatement rewrites `CREATE USER [IF NOT EXISTS] <name> PASSWORD`
// with its trailing clauses, `ALTER USER <name> PASSWORD` and `ALTER USER
// <name> ADD PASSWORD` into the VERIFIER statements the server takes, with
// the verifier as $1; add reports ADD PASSWORD, whose verifier reuses the
// salt of the user's current one. ok is false for any other statement. IF
// is the keyword only when NOT follows, since a user may be named if.
func passwordStatement(statement string) (rewritten, user string, add, ok bool) {
	words := strings.Fields(statement)
	word := func(i int) string {
		if i < len(words) {
			return strings.ToUpper(words[i])
		}
		return ""
	}
	if word(1) != "USER" {
		return "", "", false, false
	}
	name := 2
	switch word(0) {
	case "CREATE":
		if word(2) == "IF" && word(3) == "NOT" && word(4) == "EXISTS" {
			name = 5
		}
	case "ALTER":
		if word(3) == "ADD" && word(4) == "PASSWORD" {
			words[4] = "VERIFIER $1"
			return strings.Join(words, " "), words[2], true, true
		}
	default:
		return "", "", false, false
	}
	if word(name+1) != "PASSWORD" {
		return "", "", false, false
	}
	words[name+1] = "VERIFIER $1"
	return strings.Join(words, " "), words[name], false, true
}

// passwordVerifier reuses the login's SCRAM parameters for ADD PASSWORD.
func passwordVerifier(password string, add bool, info *chunkclient.ServerInfo) (string, error) {
	if add {
		return chunkclient.VerifierWithSalt(password, info.LoginSalt, info.LoginIterations)
	}
	return chunkclient.Verifier(password, chunkclient.MinIterations)
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
