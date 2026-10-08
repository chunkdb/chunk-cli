package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

const version = "1.2.0"

type globalOptions struct {
	URI           string
	TokenOverride string
	Timeout       time.Duration
	TLSInsecure   bool
	TLSServerName string
	Statement     statementOptions
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// run runs the CLI and returns the exit status.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := func(err error) int {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
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

	client, err := connect(opts)
	if err != nil {
		return fail(err)
	}
	defer func() {
		_ = client.Close()
	}()

	if shell {
		err = runShell(client, stdin, stdout, stderr, opts.Statement)
	} else {
		err = execute(client, strings.Join(rest, " "), opts.Statement, stdout)
	}
	if err != nil {
		return fail(err)
	}
	return 0
}

// connect opens the connection and sends HELLO 3.
func connect(opts globalOptions) (*chunkclient.Client, error) {
	parsedURI, err := chunkuri.Parse(opts.URI)
	if err != nil {
		return nil, err
	}
	token := parsedURI.Token
	if opts.TokenOverride != "" {
		token = opts.TokenOverride
	}
	client, err := chunkclient.Dial(chunkclient.Config{
		URI:           parsedURI,
		Timeout:       opts.Timeout,
		TLSInsecure:   opts.TLSInsecure,
		TLSServerName: opts.TLSServerName,
	})
	if err != nil {
		return nil, err
	}
	if _, err := client.Hello(token); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connecting failed: %w", err)
	}
	return client, nil
}

// runShell reads one statement per line. A server error is printed and the
// shell goes on; an error that leaves the connection unusable ends it.
func runShell(client *chunkclient.Client, input io.Reader, stdout, stderr io.Writer, defaults statementOptions) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for {
		if _, err := fmt.Fprint(stdout, "chunk> "); err != nil {
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
			err = execute(client, statement, opts, stdout)
		}
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
		}
		if broken := client.Broken(); broken != nil {
			return broken
		}
	}
}

func parseGlobalFlags(args []string, stderr io.Writer) (globalOptions, []string, error) {
	opts := globalOptions{}

	fs := flag.NewFlagSet("chunk-cli", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {}

	fs.StringVar(&opts.URI, "uri", "chunk://127.0.0.1:4242/", "connection URI: chunk://token@host:port/ or chunks://token@host:port/")
	fs.StringVar(&opts.TokenOverride, "token", "", "token override (preferred over token in URI)")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Second, "network timeout")
	fs.BoolVar(&opts.TLSInsecure, "tls-insecure", false, "allow insecure TLS certificates for chunks://")
	fs.StringVar(&opts.TLSServerName, "tls-server-name", "", "optional TLS server name override")
	fs.BoolVar(&opts.Statement.json, "json", false, "print replies as JSON")
	fs.BoolVar(&opts.Statement.blocks, "blocks", false, "print the values of every present block of a chunk")
	fs.StringVar(&opts.Statement.in, "in", "", "send the file as parameter $1")
	fs.StringVar(&opts.Statement.out, "out", "", "write the reply's bytes to the file")

	if err := fs.Parse(args); err != nil {
		return globalOptions{}, nil, err
	}
	return opts, fs.Args(), nil
}

const shellHelp = `Type one CQL statement per line; exit or quit leaves the shell.
A line may start with --json, --blocks, --in <file> or --out <file>.
`

func printUsage(w io.Writer) {
	fmt.Fprint(w, `chunk-cli `+version+`

Usage:
  chunk-cli [options] <CQL statement>
  chunk-cli [options] shell
  chunk-cli version | help

Options:
  --uri <chunk://token@host:port/ | chunks://token@host:port/>
  --token <token>
  --timeout <duration>      (default 5s)
  --tls-insecure
  --tls-server-name <name>
  --json                    print replies as JSON
  --blocks                  print the values of every present block of a chunk
  --in <file>               send the file as parameter $1 (SET CHUNK ... $1)
  --out <file>              write the reply's bytes to the file (GET CHUNK)

Examples:
  chunk-cli "CREATE TABLE world (id u16, name text(32) NULL) CHUNK 16 x 16"
  chunk-cli "SET BLOCK 10 4 IN world id = 7, name = 'door'"
  chunk-cli "GET BLOCK 10 4 FROM world"
  chunk-cli --blocks "GET CHUNK 0 0 FROM world"
  chunk-cli --out chunk.bin "GET CHUNK 0 0 FROM world"
  chunk-cli --in chunk.bin 'SET CHUNK 1 0 IN world $1'
  chunk-cli --uri chunks://token@db.example:4242/ shell
`)
}
