package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

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

	cmd := strings.ToLower(args[0])
	switch cmd {
	case "ping", "info", "auth", "get", "set", "chunk", "chunkbin":
		fmt.Printf("command %q recognized for %s (%s)\n", cmd, parsedURI.Address(), opts.URI)
		fmt.Println("network client implementation will be added in the next commit")
	case "help", "-h", "--help":
		printUsage()
	default:
		fatal(fmt.Errorf("unknown command %q", cmd))
	}
}

func parseGlobalFlags(args []string) (globalOptions, []string, error) {
	opts := globalOptions{}

	fs := flag.NewFlagSet("chunk-cli", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&opts.URI, "uri", "chunk://127.0.0.1:4242/", "connection URI: chunk://token@host:port/ or chunks://token@host:port/")
	fs.StringVar(&opts.TokenOverride, "token", "", "token override (if set, preferred over token in URI)")
	fs.DurationVar(&opts.Timeout, "timeout", 5*time.Second, "network timeout")
	fs.BoolVar(&opts.TLSInsecure, "tls-insecure", false, "allow insecure TLS certificates for chunks://")
	fs.StringVar(&opts.TLSServerName, "tls-server-name", "", "optional TLS server name override")

	if err := fs.Parse(args); err != nil {
		return globalOptions{}, nil, err
	}

	return opts, fs.Args(), nil
}

func printUsage() {
	fmt.Println(`chunk-cli ` + version + `

Usage:
  chunk-cli [global options] <command> [command args]

Commands:
  ping
  info
  auth <token>
  get <x> <y>
  set <x> <y> <bits>
  chunk <cx> <cy>
  chunkbin <cx> <cy>
  version

Global options:
  --uri <chunk://token@host:port/ | chunks://token@host:port/>
  --token <token>
  --timeout <duration>
  --tls-insecure
  --tls-server-name <name>
`)
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(1)
}
