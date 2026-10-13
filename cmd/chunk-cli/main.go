package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

const version = "1.2.0"

type globalOptions struct {
	URI            string
	User           string
	PasswordFile   string
	Timeout        time.Duration
	TLSInsecure    bool
	TLSServerName  string
	Statement      statementOptions
	SessionOptions map[string]chunkclient.Value
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run runs the CLI and returns the exit status.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var opts globalOptions
	fail := func(err error) int {
		return printError(stderr, err, opts.Statement.json)
	}
	opts, rest, err := parseGlobalFlags(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stdout)
			return 0
		}
		return fail(err)
	}
	if len(rest) == 0 {
		printUsage(stderr)
		return 1
	}

	shell := false
	if len(rest) == 1 {
		switch strings.ToLower(rest[0]) {
		case "version":
			fmt.Fprintln(stdout, version)
			return 0
		case "help":
			printUsage(stdout)
			return 0
		case "shell":
			shell = true
		}
	}

	if strings.EqualFold(rest[0], "watch") {
		watch, err := parseWatchArgs(rest[1:], opts.Statement.json)
		if err != nil {
			return fail(err)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		if err := runWatch(ctx, opts, watch, console{in: stdin, out: stderr}, stdout); err != nil {
			return fail(err)
		}
		return 0
	}

	migrationFileCommand := rest[0] == "migrate" && (len(rest) == 1 || !strings.HasPrefix(strings.TrimSpace(rest[1]), "'"))
	var migrations []migrationStep
	if migrationFileCommand || strings.EqualFold(rest[0], "migrations") {
		if err := checkMigrationOptions(opts.Statement); err != nil {
			return fail(err)
		}
		if migrationFileCommand {
			if len(rest) != 2 {
				return fail(errors.New("usage: chunk-cli [options] migrate <file>"))
			}
			migrations, err = readMigrationFile(rest[1])
			if err != nil {
				return fail(err)
			}
		} else if len(rest) != 1 {
			return fail(errors.New("usage: chunk-cli [options] migrations"))
		}
	}

	statement := strings.Join(rest, " ")
	if control := txnControlOf(statement); !shell && control != txnNone {
		return fail(fmt.Errorf("%s runs in the shell (chunk-cli shell): a transaction lives on one connection, and a one-shot statement closes its connection", control))
	}

	term := console{in: stdin, out: stderr}
	client, err := connect(opts, term)
	if err != nil {
		return fail(err)
	}
	defer func() {
		_ = client.Close()
	}()

	if migrations != nil {
		err = runMigrations(client, migrations, stdout, opts.Statement.json)
	} else if strings.EqualFold(rest[0], "migrations") {
		err = listMigrations(client, stdout, opts.Statement)
	} else if shell {
		err = runShell(client, term, stdout, opts.Statement)
	} else {
		err = execute(client, nil, statement, opts.Statement, stdout, term)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// connect opens the connection and logs in with HELLO 3.
func connect(opts globalOptions, term console) (*chunkclient.Client, error) {
	parsedURI, err := chunkuri.Parse(opts.URI)
	if err != nil {
		return nil, err
	}
	login, err := loginFor(opts, parsedURI, term)
	if err != nil {
		return nil, err
	}
	return connectLogin(opts, parsedURI, login)
}

func connectLogin(opts globalOptions, parsedURI chunkuri.Parsed, login chunkclient.Login) (*chunkclient.Client, error) {
	client, err := chunkclient.Dial(chunkclient.Config{
		URI:            parsedURI,
		Timeout:        opts.Timeout,
		TLSInsecure:    opts.TLSInsecure,
		TLSServerName:  opts.TLSServerName,
		SessionOptions: opts.SessionOptions,
	})
	if err != nil {
		return nil, err
	}
	if _, err := client.Hello(login); err != nil {
		_ = client.Close()
		if login.User == "" && errors.Is(err, chunkclient.ErrAuthRequired) {
			return nil, fmt.Errorf("connecting failed: %w (log in with --user or chunk://user:password@host/)", err)
		}
		return nil, fmt.Errorf("connecting failed: %w", err)
	}
	return client, nil
}

// runShell reads one statement per line. A server error is printed and the
// shell goes on; an error that leaves the connection unusable ends it.
// BEGIN, COMMIT and ROLLBACK run on the shell's connection, and the prompt
// shows an open transaction; leaving the shell closes the connection, which
// rolls an open transaction back.
func runShell(client *chunkclient.Client, term console, stdout io.Writer, defaults statementOptions) error {
	stderr := term.out
	txn := &shellTxn{}
	scanner := bufio.NewScanner(term.in)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for {
		prompt := "chunk> "
		if txn.open {
			prompt = "chunk*> "
		}
		if _, err := fmt.Fprint(stdout, prompt); err != nil {
			return fmt.Errorf("write prompt: %w", err)
		}
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return fmt.Errorf("read shell input: %w", err)
			}
			return nil
		}
		line := strings.TrimSpace(scanner.Text())
		switch strings.ToLower(line) {
		case "":
			continue
		case "exit", "quit":
			return nil
		case "help":
			fmt.Fprint(stdout, shellHelp)
			continue
		}
		opts, statement, err := parseLineOptions(line, defaults)
		if err == nil {
			err = execute(client, txn, statement, opts, stdout, term)
		}
		if err != nil {
			printError(stderr, err, opts.json)
		}
		if broken := client.Broken(); broken != nil {
			return broken
		}
	}
}

func parseGlobalFlags(args []string, stderr io.Writer) (globalOptions, []string, error) {
	opts := globalOptions{}

	fs := flag.NewFlagSet("chunk-cli", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // run formats the error, including JSON stderr.
	fs.Usage = func() {}

	fs.StringVar(&opts.URI, "uri", "chunk://127.0.0.1:4242/", "connection URI: chunk://user:password@host:port/ or chunks://user:password@host:port/")
	fs.StringVar(&opts.User, "user", "", "the user to log in as (preferred over the URI's user)")
	fs.StringVar(&opts.PasswordFile, "password-file", "", "read the login password from the file's first line")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Second, "network timeout")
	fs.BoolVar(&opts.TLSInsecure, "tls-insecure", false, "allow insecure TLS certificates for chunks://")
	fs.StringVar(&opts.TLSServerName, "tls-server-name", "", "optional TLS server name override")
	fs.Func("hello-option", "HELLO key=value (repeatable; unknown values are bytes)", func(text string) error {
		key, value, found := strings.Cut(text, "=")
		key = strings.ToLower(key)
		if !found || key == "" {
			return errors.New("--hello-option needs key=value")
		}
		if opts.SessionOptions == nil {
			opts.SessionOptions = map[string]chunkclient.Value{}
		}
		if _, found := opts.SessionOptions[key]; found {
			return fmt.Errorf("duplicate HELLO option %s", key)
		}
		v := chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: []byte(value)}
		if key == "statement_timeout_ms" {
			n, err := strconv.ParseUint(value, 10, 64)
			if err != nil {
				return fmt.Errorf("statement_timeout_ms: %w", err)
			}
			v = chunkclient.Value{Kind: chunkclient.KindBigNumber, Text: strconv.FormatUint(n, 10)}
		}
		opts.SessionOptions[key] = v
		return nil
	})
	fs.BoolVar(&opts.Statement.json, "json", false, "print replies as JSON")
	fs.BoolVar(&opts.Statement.blocks, "blocks", false, "print the values of every present block of a chunk")
	fs.StringVar(&opts.Statement.in, "in", "", "send the file as parameter $1")
	fs.StringVar(&opts.Statement.out, "out", "", "write the reply's bytes to the file")
	fs.StringVar(&opts.Statement.newPasswordFile, "new-password-file", "", "the password of CREATE USER / ALTER USER ... PASSWORD")

	if err := fs.Parse(args); err != nil {
		return opts, nil, err
	}
	return opts, fs.Args(), nil
}

const shellHelp = `Type one CQL statement per line; exit or quit leaves the shell.
A line may start with --json, --blocks, --in <file>, --out <file> or
--new-password-file <file>. CREATE USER <name> PASSWORD and ALTER USER <name>
PASSWORD ask for the password and send its verifier.
BEGIN starts a transaction (the prompt turns chunk*>): its reads see one
snapshot, its writes apply together at COMMIT, ROLLBACK discards them.
Streams use chunk-cli watch <table>, outside the shell.
`

func printUsage(w io.Writer) {
	fmt.Fprint(w, `chunk-cli `+version+`

Usage:
  chunk-cli [options] <CQL statement>
  chunk-cli [options] shell
  chunk-cli [options] watch <table> [watch options]
  chunk-cli [options] migrate <file>
  chunk-cli [options] migrations
  chunk-cli version | help

Options:
  --uri <chunk://user:password@host:port/ | chunks://user:password@host:port/>
  --user <name>             the user to log in as (preferred over the URI's)
  --password-file <file>    the login password, from the file's first line
                            (also CHUNKDB_PASSWORD; asked for on a terminal)
  --timeout <duration>      (default 5s)
  --tls-insecure
  --tls-server-name <name>
  --hello-option <key=value> repeatable HELLO session option
  --json                    print replies as JSON
  --blocks                  print the values of every present block of a chunk
  --in <file>               send parameter $1; SET CHUNK needs known column types
  --out <file>              write the reply's bytes to the file (GET CHUNK)
  --new-password-file <file>
                            the password of CREATE USER <name> PASSWORD and
                            ALTER USER <name> PASSWORD (asked for on a terminal)

Watch options:
  --slot <name>             durable consumer slot
  --ack-every <n>           acknowledge every n printed eligible events (default 1)
  --area <cx0,cy0,cx1,cy1>   inclusive chunk rectangle
  --columns <a,b>           projected columns
  --no-before               omit before rows
  --chunks                  stream committed chunk forms
  --after <epoch:revision>  resume from a retained position
  --takeover                replace the slot's consumer (requires --slot)
  --ack-timeout-ms <ms>      mutation ACK deadline (requires --slot)
  --json                    one JSON object per event

Examples:
  chunk-cli "CREATE TABLE world (id u16, name text(32) NULL) CHUNK 16 x 16"
  chunk-cli "SET BLOCK 10 4 IN world id = 7, name = 'door'"
  chunk-cli "GET BLOCK 10 4 FROM world"
  chunk-cli --blocks "GET CHUNK 0 0 FROM world"
  chunk-cli --out chunk.bin "GET CHUNK 0 0 FROM world"
  chunk-cli --in chunk.bin 'SET CHUNK 1 0 IN world $1'
  chunk-cli --uri chunks://admin@db.example:4242/ shell
  chunk-cli --new-password-file bot.password "CREATE USER bot PASSWORD"
  chunk-cli "GRANT READ ON world TO bot"
`)
}
