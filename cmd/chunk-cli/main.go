package main

import (
	"encoding/hex"
	"flag"
	"fmt"
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
		fatal(err)
	}

	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	if args[0] == "version" {
		fmt.Println(version)
		return
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

	cmd := strings.ToLower(args[0])
	cmdArgs := args[1:]

	if cmd != "auth" && effectiveToken != "" {
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
		output := string(payload)
		if !strings.HasSuffix(output, "\n") {
			output += "\n"
		}
		fmt.Print(output)
	case "auth":
		token := ""
		if len(cmdArgs) > 0 {
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
		x, y := mustTwoCoords(cmdArgs, "get")
		payload, err := runBulk(client, fmt.Sprintf("GET %s %s", x, y))
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(payload))
	case "set":
		if len(cmdArgs) != 3 {
			fatal(fmt.Errorf("usage: set <x> <y> <bits>"))
		}
		mustInt(cmdArgs[0], "x")
		mustInt(cmdArgs[1], "y")
		bits := cmdArgs[2]
		if bits == "" {
			fatal(fmt.Errorf("bits must not be empty"))
		}
		text, err := runSimple(client, fmt.Sprintf("SET %s %s %s", cmdArgs[0], cmdArgs[1], bits))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "chunk":
		cx, cy := mustTwoCoords(cmdArgs, "chunk")
		payload, err := runBulk(client, fmt.Sprintf("CHUNK %s %s", cx, cy))
		if err != nil {
			fatal(err)
		}
		fmt.Println(string(payload))
	case "chunkbin":
		if err := runChunkBin(client, cmdArgs); err != nil {
			fatal(err)
		}
	case "help", "-h", "--help":
		printUsage()
	default:
		fatal(fmt.Errorf("unknown command %q", cmd))
	}
}

func runChunkBin(client *chunkclient.Client, cmdArgs []string) error {
	fs := flag.NewFlagSet("chunkbin", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

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
	mustInt(cx, "cx")
	mustInt(cy, "cy")

	payload, err := runBulk(client, fmt.Sprintf("CHUNKBIN %s %s", cx, cy))
	if err != nil {
		return err
	}

	if *outPath != "" {
		if err := os.WriteFile(*outPath, payload, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *outPath, err)
		}
		fmt.Printf("wrote %d bytes to %s\n", len(payload), *outPath)
		return nil
	}

	fmt.Printf("bytes=%d\n", len(payload))
	fmt.Print(hex.Dump(payload))
	return nil
}

func runSimple(client *chunkclient.Client, command string) (string, error) {
	resp, err := client.Command(command)
	if err != nil {
		return "", err
	}
	if resp.Kind != chunkclient.ResponseSimple {
		return "", fmt.Errorf("unexpected response kind for %q", command)
	}
	return resp.Simple, nil
}

func runBulk(client *chunkclient.Client, command string) ([]byte, error) {
	resp, err := client.Command(command)
	if err != nil {
		return nil, err
	}
	if resp.Kind != chunkclient.ResponseBulk {
		return nil, fmt.Errorf("unexpected response kind for %q", command)
	}
	return resp.Bulk, nil
}

func mustTwoCoords(args []string, command string) (string, string) {
	if len(args) != 2 {
		fatal(fmt.Errorf("usage: %s <x> <y>", command))
	}
	mustInt(args[0], "x")
	mustInt(args[1], "y")
	return args[0], args[1]
}

func mustInt(value string, field string) {
	if _, err := strconv.ParseInt(value, 10, 64); err != nil {
		fatal(fmt.Errorf("invalid %s %q: %w", field, value, err))
	}
}

func parseGlobalFlags(args []string) (globalOptions, []string, error) {
	opts := globalOptions{}

	fs := flag.NewFlagSet("chunk-cli", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&opts.URI, "uri", "chunk://127.0.0.1:4242/", "connection URI: chunk://token@host:port/ or chunks://token@host:port/")
	fs.StringVar(&opts.TokenOverride, "token", "", "token override (preferred over token in URI)")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Second, "network timeout")
	fs.BoolVar(&opts.TLSInsecure, "tls-insecure", false, "allow insecure TLS certificates for chunks://")
	fs.StringVar(&opts.TLSServerName, "tls-server-name", "", "optional TLS server name override")

	if err := fs.Parse(args); err != nil {
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
  chunk-cli --uri chunks://token@127.0.0.1:4242/ --tls-insecure info
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
