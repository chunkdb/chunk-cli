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

const version = "1.1.0"

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
	case "ping", "info", "auth", "get", "exists", "set", "unset", "mset", "mget", "chunkexists", "chunkset", "chunkstate", "chunksetstate", "chunk", "chunkbin", "chunkbinstate", "chunkbinc", "chunkbincstate", "chunkscan", "chunkrange", "chunkradius", "chunkver", "chunkcas", "chunkbatch", "walflush", "metrics", "shell":
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
	case "exists":
		text, err := runSimple(client, fmt.Sprintf("EXISTS %s %s", cmdArgs[0], cmdArgs[1]))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "set":
		text, err := runSimple(client, fmt.Sprintf("SET %s %s %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "unset":
		text, err := runSimple(client, fmt.Sprintf("UNSET %s %s", cmdArgs[0], cmdArgs[1]))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "mset":
		text, err := runSimple(client, "MSET "+strings.Join(cmdArgs, " "))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "mget":
		items, err := runArray(client, "MGET "+strings.Join(cmdArgs, " "))
		if err != nil {
			fatal(err)
		}
		for _, item := range items {
			fmt.Println(string(item))
		}
	case "chunkexists":
		text, err := runSimple(client, fmt.Sprintf("CHUNKEXISTS %s %s", cmdArgs[0], cmdArgs[1]))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "chunkset":
		text, err := runSimple(client, fmt.Sprintf("CHUNKSET %s %s %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "chunkstate":
		cx, cy := cmdArgs[0], cmdArgs[1]
		payload, err := runBulk(client, fmt.Sprintf("CHUNK %s %s STATE", cx, cy))
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "chunksetstate":
		text, err := runSimple(client, fmt.Sprintf("CHUNKSET %s %s STATE %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
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
		if err := runChunkBinLike(client, "CHUNKBIN", false, "chunkbin", cmdArgs, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	case "chunkbinstate":
		if err := runChunkBinLike(client, "CHUNKBIN", true, "chunkbinstate", cmdArgs, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	case "chunkbinc":
		if err := runChunkBinCompressed(client, false, "chunkbinc", cmdArgs, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	case "chunkbincstate":
		if err := runChunkBinCompressed(client, true, "chunkbincstate", cmdArgs, os.Stdout, os.Stderr); err != nil {
			fatal(err)
		}
	case "chunkradius":
		items, err := runArray(client, "CHUNKRADIUS "+strings.Join(cmdArgs, " "))
		if err != nil {
			fatal(err)
		}
		for _, item := range items {
			fmt.Println(string(item))
		}
	case "chunkscan":
		items, err := runArray(client, "CHUNKSCAN "+strings.Join(cmdArgs, " "))
		if err != nil {
			fatal(err)
		}
		for _, item := range items {
			fmt.Println(string(item))
		}
	case "chunkrange":
		items, err := runArray(client, "CHUNKRANGE "+strings.Join(cmdArgs, " "))
		if err != nil {
			fatal(err)
		}
		for _, item := range items {
			fmt.Println(string(item))
		}
	case "chunkver":
		payload, err := runBulk(client, fmt.Sprintf("CHUNKVER %s %s", cmdArgs[0], cmdArgs[1]))
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "chunkcas":
		payload, err := runBulk(client, fmt.Sprintf("CHUNKCAS %s %s %s STATE %s", cmdArgs[0], cmdArgs[1], cmdArgs[2], cmdArgs[3]))
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "chunkbatch":
		payload, err := runBulk(client, "CHUNKBATCH "+strings.Join(cmdArgs, " "))
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
	case "walflush":
		text, err := runSimple(client, "WALFLUSH")
		if err != nil {
			fatal(err)
		}
		fmt.Println(text)
	case "metrics":
		payload, err := runBulk(client, "METRICS")
		if err != nil {
			fatal(err)
		}
		printTextPayload(os.Stdout, payload)
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
	case "exists":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: exists <x> <y>")
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
	case "unset":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: unset <x> <y>")
		}
		if err := validateIntArg(cmdArgs[0], "x"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "y"); err != nil {
			return err
		}
	case "mset":
		if len(cmdArgs) == 0 || len(cmdArgs)%3 != 0 {
			return fmt.Errorf("usage: mset <x> <y> <bits> [<x> <y> <bits> ...]")
		}
		for i := 0; i < len(cmdArgs); i += 3 {
			if err := validateIntArg(cmdArgs[i], "x"); err != nil {
				return err
			}
			if err := validateIntArg(cmdArgs[i+1], "y"); err != nil {
				return err
			}
			if cmdArgs[i+2] == "" {
				return fmt.Errorf("bits must not be empty")
			}
			if err := validateBits(cmdArgs[i+2]); err != nil {
				return err
			}
		}
	case "mget":
		if len(cmdArgs) == 0 || len(cmdArgs)%2 != 0 {
			return fmt.Errorf("usage: mget <x> <y> [<x> <y> ...]")
		}
		for i := 0; i < len(cmdArgs); i += 2 {
			if err := validateIntArg(cmdArgs[i], "x"); err != nil {
				return err
			}
			if err := validateIntArg(cmdArgs[i+1], "y"); err != nil {
				return err
			}
		}
	case "chunkexists":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: chunkexists <cx> <cy>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
	case "chunkset":
		if len(cmdArgs) != 3 {
			return fmt.Errorf("usage: chunkset <cx> <cy> <bits>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
		if cmdArgs[2] == "" {
			return fmt.Errorf("bits must not be empty")
		}
		if err := validateBits(cmdArgs[2]); err != nil {
			return err
		}
	case "chunkstate":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: chunkstate <cx> <cy>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
	case "chunksetstate":
		if len(cmdArgs) != 3 {
			return fmt.Errorf("usage: chunksetstate <cx> <cy> <payload_bits>|<presence_bits>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
		if err := validateChunkState(cmdArgs[2]); err != nil {
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
	case "chunkbin":
		return validateChunkBinLikeArgs("chunkbin", cmdArgs, stderrDiscard{})
	case "chunkbinstate":
		return validateChunkBinLikeArgs("chunkbinstate", cmdArgs, stderrDiscard{})
	case "chunkbinc":
		return validateChunkBinCompressedArgs("chunkbinc", cmdArgs, stderrDiscard{})
	case "chunkbincstate":
		return validateChunkBinCompressedArgs("chunkbincstate", cmdArgs, stderrDiscard{})
	case "chunkradius":
		if len(cmdArgs) != 3 {
			return fmt.Errorf("usage: chunkradius <cx> <cy> <radius_chunks>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
		if _, err := strconv.ParseUint(cmdArgs[2], 10, 64); err != nil {
			return fmt.Errorf("invalid radius_chunks %q: %w", cmdArgs[2], err)
		}
	case "chunkscan":
		if len(cmdArgs) != 1 && len(cmdArgs) != 3 {
			return fmt.Errorf("usage: chunkscan <limit> [<cursor_cx> <cursor_cy>]")
		}
		if _, err := strconv.ParseUint(cmdArgs[0], 10, 64); err != nil {
			return fmt.Errorf("invalid limit %q: %w", cmdArgs[0], err)
		}
		if len(cmdArgs) == 3 {
			if err := validateIntArg(cmdArgs[1], "cursor_cx"); err != nil {
				return err
			}
			if err := validateIntArg(cmdArgs[2], "cursor_cy"); err != nil {
				return err
			}
		}
	case "chunkrange":
		if len(cmdArgs) != 4 {
			return fmt.Errorf("usage: chunkrange <cx0> <cy0> <cx1> <cy1>")
		}
		for i, field := range []string{"cx0", "cy0", "cx1", "cy1"} {
			if err := validateIntArg(cmdArgs[i], field); err != nil {
				return err
			}
		}
	case "chunkver":
		if len(cmdArgs) != 2 {
			return fmt.Errorf("usage: chunkver <cx> <cy>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
	case "chunkcas":
		if len(cmdArgs) != 4 {
			return fmt.Errorf("usage: chunkcas <cx> <cy> <version> <payload_bits>|<presence_bits>")
		}
		if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
			return err
		}
		if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
			return err
		}
		if _, err := strconv.ParseUint(cmdArgs[2], 10, 64); err != nil {
			return fmt.Errorf("invalid version %q: %w", cmdArgs[2], err)
		}
		if err := validateChunkState(cmdArgs[3]); err != nil {
			return err
		}
	case "chunkbatch":
		if err := validateChunkBatchArgs(cmdArgs); err != nil {
			return err
		}
	case "walflush", "metrics":
		if len(cmdArgs) != 0 {
			return fmt.Errorf("usage: %s", cmd)
		}
	case "shell":
		if len(cmdArgs) != 0 {
			return fmt.Errorf("usage: shell")
		}
	}
	return nil
}

func validateChunkBatchArgs(cmdArgs []string) error {
	const usage = "usage: chunkbatch <cx> <cy> <version|-> SET <x> <y> <bits> | UNSET <x> <y> ..."
	if len(cmdArgs) < 5 {
		return fmt.Errorf("%s", usage)
	}
	if err := validateIntArg(cmdArgs[0], "cx"); err != nil {
		return err
	}
	if err := validateIntArg(cmdArgs[1], "cy"); err != nil {
		return err
	}
	if cmdArgs[2] != "-" {
		if _, err := strconv.ParseUint(cmdArgs[2], 10, 64); err != nil {
			return fmt.Errorf("invalid version %q: %w", cmdArgs[2], err)
		}
	}
	i := 3
	for i < len(cmdArgs) {
		switch strings.ToLower(cmdArgs[i]) {
		case "set":
			if i+3 > len(cmdArgs)-1 {
				return fmt.Errorf("%s", usage)
			}
			if err := validateIntArg(cmdArgs[i+1], "x"); err != nil {
				return err
			}
			if err := validateIntArg(cmdArgs[i+2], "y"); err != nil {
				return err
			}
			if err := validateBits(cmdArgs[i+3]); err != nil {
				return err
			}
			i += 4
		case "unset":
			if i+2 > len(cmdArgs)-1 {
				return fmt.Errorf("%s", usage)
			}
			if err := validateIntArg(cmdArgs[i+1], "x"); err != nil {
				return err
			}
			if err := validateIntArg(cmdArgs[i+2], "y"); err != nil {
				return err
			}
			i += 3
		default:
			return fmt.Errorf("batch operations must be SET or UNSET, got %q", cmdArgs[i])
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

func validateChunkState(state string) error {
	if state == "" {
		return fmt.Errorf("chunk state must not be empty")
	}
	separator := strings.Index(state, "|")
	if separator <= 0 || separator != strings.LastIndex(state, "|") || separator == len(state)-1 {
		return fmt.Errorf("chunk state must be <payload_bits>|<presence_bits>")
	}
	if err := validateBits(state[:separator]); err != nil {
		return err
	}
	if err := validateBits(state[separator+1:]); err != nil {
		return err
	}
	return nil
}

type stderrDiscard struct{}

func (stderrDiscard) Write(p []byte) (int, error) {
	return len(p), nil
}

func validateChunkBinLikeArgs(name string, cmdArgs []string, stderr io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	_ = fs.String("out", "", "write raw binary payload to file")

	if err := fs.Parse(cmdArgs); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: %s [--out <file>] <cx> <cy>", name)
	}

	cx := remaining[0]
	cy := remaining[1]

	if err := validateIntArg(cx, "cx"); err != nil {
		return err
	}
	if err := validateIntArg(cy, "cy"); err != nil {
		return err
	}

	return nil
}

func validateChunkBinCompressedArgs(name string, cmdArgs []string, stderr io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	_ = fs.String("out", "", "write decompressed binary payload to file")
	_ = fs.Bool("raw", false, "write the raw compressed payload instead of decompressing")

	if err := fs.Parse(cmdArgs); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: %s [--out <file>] [--raw] <cx> <cy>", name)
	}

	if err := validateIntArg(remaining[0], "cx"); err != nil {
		return err
	}
	if err := validateIntArg(remaining[1], "cy"); err != nil {
		return err
	}

	return nil
}

func runChunkBinLike(client *chunkclient.Client, command string, state bool, name string, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	outPath := fs.String("out", "", "write raw binary payload to file")

	if err := fs.Parse(cmdArgs); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: %s [--out <file>] <cx> <cy>", name)
	}

	cx := remaining[0]
	cy := remaining[1]

	if err := validateIntArg(cx, "cx"); err != nil {
		return err
	}
	if err := validateIntArg(cy, "cy"); err != nil {
		return err
	}

	request := fmt.Sprintf("%s %s %s", command, cx, cy)
	if state {
		request += " STATE"
	}

	payload, err := runBulk(client, request)
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

// runChunkBinCompressed sends CHUNKBINC, decompresses the zrle payload
// client-side, and presents the same decompressed bytes as chunkbin so
// compressed reads are usable without a separate tool.
func runChunkBinCompressed(client *chunkclient.Client, state bool, name string, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)

	outPath := fs.String("out", "", "write decompressed binary payload to file")
	rawFlag := fs.Bool("raw", false, "write the raw compressed payload instead of decompressing")

	if err := fs.Parse(cmdArgs); err != nil {
		return err
	}

	remaining := fs.Args()
	if len(remaining) != 2 {
		return fmt.Errorf("usage: %s [--out <file>] [--raw] <cx> <cy>", name)
	}

	cx := remaining[0]
	cy := remaining[1]
	if err := validateIntArg(cx, "cx"); err != nil {
		return err
	}
	if err := validateIntArg(cy, "cy"); err != nil {
		return err
	}

	request := fmt.Sprintf("CHUNKBINC %s %s", cx, cy)
	if state {
		request += " STATE"
	}

	compressed, err := runBulk(client, request)
	if err != nil {
		return err
	}

	payload := compressed
	if !*rawFlag {
		payload, err = decodeZrle(compressed)
		if err != nil {
			return err
		}
	}

	if *outPath != "" {
		if err := os.WriteFile(*outPath, payload, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", *outPath, err)
		}
		fmt.Fprintf(stdout, "wrote %d bytes to %s\n", len(payload), *outPath)
		return nil
	}

	if *rawFlag {
		fmt.Fprintf(stdout, "compressed_bytes=%d\n", len(payload))
	} else {
		fmt.Fprintf(stdout, "bytes=%d compressed_bytes=%d\n", len(payload), len(compressed))
	}
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
		case "exists":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, fmt.Sprintf("EXISTS %s %s", cmdArgs[0], cmdArgs[1]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
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
		case "unset":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, fmt.Sprintf("UNSET %s %s", cmdArgs[0], cmdArgs[1]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
		case "mset":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, "MSET "+strings.Join(cmdArgs, " "))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
		case "mget":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			items, err := runArray(client, "MGET "+strings.Join(cmdArgs, " "))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			for _, item := range items {
				fmt.Fprintln(stdout, string(item))
			}
		case "chunkexists":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, fmt.Sprintf("CHUNKEXISTS %s %s", cmdArgs[0], cmdArgs[1]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
		case "chunkset":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, fmt.Sprintf("CHUNKSET %s %s %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
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
			if err := runChunkBinLike(client, "CHUNKBIN", false, "chunkbin", cmdArgs, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
		case "chunkstate":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			payload, err := runBulk(client, fmt.Sprintf("CHUNK %s %s STATE", cmdArgs[0], cmdArgs[1]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			printTextPayload(stdout, payload)
		case "chunksetstate":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, fmt.Sprintf("CHUNKSET %s %s STATE %s", cmdArgs[0], cmdArgs[1], cmdArgs[2]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
		case "chunkbinstate":
			if err := runChunkBinLike(client, "CHUNKBIN", true, "chunkbinstate", cmdArgs, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
		case "chunkbinc":
			if err := runChunkBinCompressed(client, false, "chunkbinc", cmdArgs, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
		case "chunkbincstate":
			if err := runChunkBinCompressed(client, true, "chunkbincstate", cmdArgs, stdout, stderr); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
			}
		case "chunkscan", "chunkrange", "chunkradius":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			items, err := runArray(client, strings.ToUpper(cmd)+" "+strings.Join(cmdArgs, " "))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			for _, item := range items {
				fmt.Fprintln(stdout, string(item))
			}
		case "chunkver", "chunkbatch", "metrics":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			command := strings.ToUpper(cmd)
			if len(cmdArgs) > 0 {
				command += " " + strings.Join(cmdArgs, " ")
			}
			payload, err := runBulk(client, command)
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			printTextPayload(stdout, payload)
		case "chunkcas":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			payload, err := runBulk(client, fmt.Sprintf("CHUNKCAS %s %s %s STATE %s", cmdArgs[0], cmdArgs[1], cmdArgs[2], cmdArgs[3]))
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			printTextPayload(stdout, payload)
		case "walflush":
			if err := validateCommandArgs(cmd, cmdArgs); err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			text, err := runSimple(client, "WALFLUSH")
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				continue
			}
			fmt.Fprintln(stdout, text)
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

func runArray(client *chunkclient.Client, command string) ([][]byte, error) {
	resp, err := client.Command(command)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", commandVerb(command), err)
	}
	if resp.Kind != chunkclient.ResponseArray {
		return nil, fmt.Errorf("%s failed: expected array response", commandVerb(command))
	}
	return resp.Array, nil
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
  exists <x> <y>
  set <x> <y> <bits>
  unset <x> <y>
  mset <x> <y> <bits> [<x> <y> <bits> ...]
  mget <x> <y> [<x> <y> ...]
  chunkexists <cx> <cy>
  chunkset <cx> <cy> <bits>
  chunkstate <cx> <cy>
  chunksetstate <cx> <cy> <payload_bits>|<presence_bits>
  chunk <cx> <cy>
  chunkbin [--out <file>] <cx> <cy>
  chunkbinstate [--out <file>] <cx> <cy>
  chunkbinc [--out <file>] [--raw] <cx> <cy>
  chunkbincstate [--out <file>] [--raw] <cx> <cy>
  chunkscan <limit> [<cursor_cx> <cursor_cy>]
  chunkrange <cx0> <cy0> <cx1> <cy1>
  chunkradius <cx> <cy> <radius_chunks>
  chunkver <cx> <cy>
  chunkcas <cx> <cy> <version> <payload_bits>|<presence_bits>
  chunkbatch <cx> <cy> <version|-> SET <x> <y> <bits> | UNSET <x> <y> ...
  walflush
  metrics
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
  chunk-cli --uri chunk://token@127.0.0.1:4242/ exists 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunkexists 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunkset 0 0 <full_chunk_bits>
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunkstate 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunksetstate 0 0 <payload_bits>|<presence_bits>
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
