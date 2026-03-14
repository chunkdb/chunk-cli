package main

import (
	"bufio"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

const version = "0.1.0-alpha"

type globalOptions struct {
	URI           string
	TokenOverride string
	Timeout       time.Duration
	TLSInsecure   bool
	TLSServerName string
}

func main() {
	opts, args, err := parseGlobalFlags(os.Args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage()
			return
		}
		fatal(err)
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	cmd := strings.ToLower(args[0])
	cmdArgs := args[1:]

	switch cmd {
	case "version":
		fmt.Println(version)
		return
	case "help", "-h", "--help":
		printUsage()
		return
	case "ping", "info", "auth", "get", "set", "chunk", "chunkbin", "shell":
		// network command
	default:
		fatal(fmt.Errorf("unknown command %q", cmd))
	}

	if err := validateCommandArgs(cmd, cmdArgs); err != nil {
		fatal(err)
	}

	parsedURI, err := chunkuri.Parse(opts.URI)
	if err != nil {
		fatal(err)
	}

	effectiveToken := parsedURI.Token
	if opts.TokenOverride != "" {
		effectiveToken = opts.TokenOverride
	}

	client, err := chunkclient.Dial(chunkclient.Config{
		URI:           parsedURI,
		Timeout:       opts.Timeout,
		TLSInsecure:   opts.TLSInsecure,
		TLSServerName: opts.TLSServerName,
	})
	if err != nil {
		fatal(err)
	}
	defer func() {
		_ = client.Close()
	}()

	if cmd != "auth" && cmd != "shell" && effectiveToken != "" {
		if _, err := runSimple(client, "AUTH "+effectiveToken); err != nil {
			fatal(fmt.Errorf("automatic AUTH failed: %w", err))
		}
	}

	switch cmd {
	case "ping":
		text, err := runSimple(client, "PING")
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "info":
		payload, err := runBulk(client, "INFO")
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "auth":
		token := ""
		if len(cmdArgs) == 1 {
			token = cmdArgs[0]
		} else {
			token = effectiveToken
		}
		if token == "" {
			fatal(fmt.Errorf("auth token required: use `auth <token>` or provide token in --uri/--token"))
		}
		text, err := runSimple(client, "AUTH "+token)
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "get":
		x, y := cmdArgs[0], cmdArgs[1]
		payload, err := runBulk(client, fmt.Sprintf("GET %s %s", x, y))
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "set":
		text, err := runSimple(client, fmt.Sprintf("SET %s %s %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "chunk":
		cx, cy := cmdArgs[0], cmdArgs[1]
		payload, err := runBulk(client, fmt.Sprintf("CHUNK %s %s", cx, cy))
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "chunkbin":
		if err := runChunkBin(client, cmdArgs, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	case "shell":
		if err := runShell(client, effectiveToken, os.Stdin, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	}
}

func validateCommandArgs(cmd string, cmdArgs []string) error {
	switch cmd {
	case "ping", "info":
		if len(cmdArgs) != 0 {
			return fmt.Errorf("usage: %s", cmd)
		}
	case "auth":
		if len(cmdArgs) > 1 {
			return fmt.Errorf("usage: auth <token>")
		}
	case "get":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: get <x> <y>")
		}
		if err := validateIntArg(cmdArgs[0], "x"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "y"); err != nil {
			return err
		}
	case "set":
		if len(cmdArgs) != 3 {
			return fmt.Errorf("usage: set <x> <y> <bits>")
		}
		if err := validateIntArg(cmdArgs[0], "x"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "y"); err != nil {
			return err
		}
		if cmdArgs[2] == "" {
			return fmt.Errorf("bits must not be empty")
		}
		if err := validateBits(cmdArgs[2]); err != nil {
			return err
		}
	case "chunk":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: chunk <cx> <cy>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
	case "shell":
		if len(cmdArgs) != 0 {
			return fmt.Errorf("usage: shell")
		}
	}
	return nil
}

func validateIntArg(value string, field string) error {
	if _, err := strconv.ParseInt(value, 10, 64); err != nil {
		return fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return nil
}

func runChunkBin(client *chunkclient.Client, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("chunkbin", flag.ContinueOnError)
	fs.SetOutput(stderr)

	outPath := fs.String("out", "", "write raw binary payload to file")

	if err := fs.Parse(cmdArgs); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: chunkbin [--out <file>] <cx> <cy>")
	}

	cx := remaining[0]
	cy := remaining[1]

	if err := validateIntArg(cx, "cx"); err != nil {
		return err
	}
	if err := validateIntArg(cy, "cy"); err != nil {
		return err
	}

	payload, err := runBulk(client, fmt.Sprintf("CHUNKBIN %s %s", cx, cy))
	if err != nil {
		return err
	}

	if *outPath != "" {
		if err := os.WriteFile(*outPath, payload, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *outPath, err)
		}
		fmt.Fprintf(stdout, "wrote %d bytes to %s\n", len(payload), *outPath)
		return nil
	}

	fmt.Fprintf(stdout, "bytes=%d\n", len(payload))
	fmt.Fprint(stdout, hex.Dump(payload))
	return nil
}

func runShell(
	client *chunkclient.Client,
	defaultToken string,
	input io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if defaultToken != "" {
		if _, err := runSimple(client, "AUTH "+defaultToken); err != nil {
			return fmt.Errorf("automatic AUTH failed: %w", err)
		}
	}

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
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		cmd := strings.ToLower(fields[0])
		cmdArgs := fields[1:]

		switch cmd {
		case "exit":
			return nil
		case "quit":
			text, err := runSimple(client, "QUIT")
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return nil
			}
			fmt.Fprintln(stdout, text)
			return nil
		case "ping":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, "PING")
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
		case "info":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			payload, err := runBulk(client, "INFO")
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			printTextPayload(stdout, payload)
		case "auth":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}

			token := defaultToken
			if len(cmdArgs) == 1 {
				token = cmdArgs[0]
			}
			if token == "" {
				fmt.Fprintln(stderr, "error: auth token required: use `auth <token>` or provide token in --uri/--token")
				continue
			}

			text, err := runSimple(client, "AUTH "+token)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			defaultToken = token
			fmt.Fprintln(stdout, text)
		case "get":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			payload, err := runBulk(client, fmt.Sprintf("GET %s %s", cmdArgs[0], cmdArgs[1]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			printTextPayload(stdout, payload)
		case "set":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, fmt.Sprintf("SET %s %s %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
		case "chunk":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			payload, err := runBulk(client, fmt.Sprintf("CHUNK %s %s", cmdArgs[0], cmdArgs[1]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			printTextPayload(stdout, payload)
		case "chunkbin":
			if err := runChunkBin(client, cmdArgs, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
		default:
			fmt.Fprintf(stderr, "error: unknown shell command %q\n", cmd)
		}
	}
}

func runSimple(client *chunkclient.Client, command string) (string, error) {
	resp, err := client.Command(command)
	if err != nil {
		return "", fmt.Errorf("%s failed: %w", commandVerb(command), err)
	}
	if resp.Kind != chunkclient.ResponseSimple {
		return "", fmt.Errorf("%s failed: expected simple response", commandVerb(command))
	}
	return resp.Simple, nil
}

func runBulk(client *chunkclient.Client, command string) ([]byte, error) {
	resp, err := client.Command(command)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", commandVerb(command), err)
	}
	if resp.Kind != chunkclient.ResponseBulk {
		return nil, fmt.Errorf("%s failed: expected bulk response", commandVerb(command))
	}
	return resp.Bulk, nil
}

func commandVerb(command string) string {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return "command"
	}
	return strings.ToLower(fields[0])
}

func validateBits(bits string) error {
	for i, ch := range bits {
		if ch != '0' && ch != '1' {
			return fmt.Errorf("invalid bits: only '0' and '1' are allowed (position %d)", i)
		}
	}
	return nil
}

func parseGlobalFlags(args []string) (globalOptions, []string, error) {
	opts := globalOptions{}

	fs := flag.NewFlagSet("chunk-cli", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {}

	fs.StringVar(&opts.URI, "uri", "chunk://127.0.0.1:4242/", "connection URI: chunk://token@host:port/ or chunks://token@host:port/")
	fs.StringVar(&opts.TokenOverride, "token", "", "token override (preferred over token in URI)")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Second, "network timeout")
	fs.BoolVar(&opts.TLSInsecure, "tls-insecure", false, "allow insecure TLS certificates for chunks://")
	fs.StringVar(&opts.TLSServerName, "tls-server-name", "", "optional TLS server name override")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return globalOptions{}, nil, flag.ErrHelp
		}
		return globalOptions{}, nil, err
	}

	return opts, fs.Args(), nil
}

func printUsage() {
	fmt.Print(`chunk-cli ` + version + `

Usage:
  chunk-cli [global options] <command> [command args]

Commands:
  ping
  info
  auth <token>
  get <x> <y>
  set <x> <y> <bits>
  chunk <cx> <cy>
  chunkbin [--out <file>] <cx> <cy>
  shell
  version

Global options:
  --uri <chunk://token@host:port/ | chunks://token@host:port/>
  --token <token>
  --timeout <duration>
  --tls-insecure
  --tls-server-name <name>

Examples:
  chunk-cli --uri chunk://token@127.0.0.1:4242/ ping
  chunk-cli --uri chunk://token@127.0.0.1:4242/ get 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ shell
  chunk-cli --uri chunks://token@127.0.0.1:4242/ --tls-insecure info
`)
}

func printTextPayload(out io.Writer, payload []byte) {
	output := string(payload)
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	fmt.Fprint(out, output)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
