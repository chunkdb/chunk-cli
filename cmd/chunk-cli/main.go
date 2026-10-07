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

const version = "1.2.0"

type globalOptions struct {
	URI           string
	TokenOverride string
	Table         string
	Timeout       time.Duration
	TLSInsecure   bool
	TLSServerName string
}

// networkCommands are the commands that talk to a server.
var networkCommands = map[string]bool{
	"ping": true, "info": true, "get": true, "set": true, "unset": true, "mset": true, "mget": true,
	"xget": true, "xput": true, "xdel": true,
	"chunkexists": true, "chunk": true, "chunkstate": true, "chunkset": true, "chunksetstate": true,
	"chunkget": true, "chunkput": true, "chunkscan": true, "chunkrange": true, "chunkradius": true,
	"chunkver": true, "chunkbatch": true, "walflush": true, "metrics": true,
	"tables": true, "tableinfo": true, "use": true, "tablecreate": true, "tableset": true, "tabledrop": true,
	"shell": true,
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
	}
	if !networkCommands[cmd] {
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
	table := parsedURI.Table
	if opts.Table != "" {
		table = opts.Table
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

	sess, err := openSession(client, effectiveToken, table)
	if err != nil {
		fatal(err)
	}
	if cmd == "shell" {
		err = runShell(sess, os.Stdin, os.Stdout, os.Stderr)
	} else {
		err = runCommand(sess, cmd, cmdArgs, os.Stdout, os.Stderr)
	}
	if err != nil {
		fatal(err)
	}
}

// geometry is the selected table's chunk layout, which the chunk commands
// need to convert between bytes and bit text and to check sizes.
type geometry struct {
	blockBits int
	width     int
	height    int
}

func (g geometry) blockCount() int    { return g.width * g.height }
func (g geometry) payloadBits() int   { return g.blockCount() * g.blockBits }
func (g geometry) payloadBytes() int  { return (g.payloadBits() + 7) / 8 }
func (g geometry) presenceBytes() int { return (g.blockCount() + 7) / 8 }

// session is a connection after HELLO and the table it works on.
type session struct {
	client *chunkclient.Client
	// table is the selected table, empty when the connection has none (the
	// server has no `default` table and none was named).
	table string
	// named is true once a table was named (--table, the URI path or use);
	// the shell prompt shows it then.
	named    bool
	geometry *geometry
	// maxExtraChunkBytes is HELLO's max_extra_chunk_bytes, 0 when the server
	// has no extra data.
	maxExtraChunkBytes int
}

func openSession(client *chunkclient.Client, token string, table string) (*session, error) {
	info, err := client.Hello(token, table)
	if err != nil {
		if table != "" {
			return nil, fmt.Errorf("connecting with table %q failed: %w", table, err)
		}
		return nil, fmt.Errorf("connecting failed: %w", err)
	}
	sess := &session{client: client, named: table != ""}
	if text, ok := info["max_extra_chunk_bytes"]; ok {
		limit, err := strconv.Atoi(text)
		if err != nil || limit <= 0 {
			return nil, fmt.Errorf("HELLO reply has no valid max_extra_chunk_bytes: %q", text)
		}
		sess.maxExtraChunkBytes = limit
	}
	if err := sess.adopt(info); err != nil {
		return nil, err
	}
	return sess, nil
}

// adopt takes the table and geometry from a HELLO or USE reply.
func (s *session) adopt(info map[string]string) error {
	name, ok := info["table"]
	if !ok {
		s.table = ""
		s.geometry = nil
		return nil
	}
	g := geometry{}
	for _, field := range []struct {
		key string
		dst *int
	}{{"block_bits", &g.blockBits}, {"chunk_width_blocks", &g.width}, {"chunk_height_blocks", &g.height}} {
		value, err := strconv.Atoi(info[field.key])
		if err != nil || value <= 0 {
			return fmt.Errorf("table %q info has no valid %s", name, field.key)
		}
		*field.dst = value
	}
	s.table = name
	s.geometry = &g
	return nil
}

func (s *session) requireGeometry() (geometry, error) {
	if s.geometry == nil {
		return geometry{}, errors.New("no table selected: the server has no default table; select one with use <table>")
	}
	return *s.geometry, nil
}

// runCommand runs one network command (validated by validateCommandArgs) and
// prints its result.
func runCommand(s *session, cmd string, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	client := s.client
	switch cmd {
	case "ping", "walflush", "set", "unset", "mset", "chunkexists", "xdel":
		command := strings.ToUpper(cmd)
		if len(cmdArgs) > 0 {
			command += " " + strings.Join(cmdArgs, " ")
		}
		text, err := runSimple(client, command)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, text)
	case "info", "metrics", "chunkver":
		command := strings.ToUpper(cmd)
		if len(cmdArgs) > 0 {
			command += " " + strings.Join(cmdArgs, " ")
		}
		payload, err := runBulk(client, command)
		if err != nil {
			return err
		}
		printTextPayload(stdout, payload)
	case "get":
		payload, err := runBulkOrNull(client, fmt.Sprintf("GET %s %s", cmdArgs[0], cmdArgs[1]))
		if err != nil {
			return err
		}
		printBlock(stdout, payload)
	case "mget":
		items, err := runArray(client, "MGET "+strings.Join(cmdArgs, " "))
		if err != nil {
			return err
		}
		if len(items) != len(cmdArgs)/2 {
			return fmt.Errorf("mget failed: %d items for %d blocks", len(items), len(cmdArgs)/2)
		}
		for _, item := range items {
			printBlock(stdout, item)
		}
	case "xget":
		return runXGet(s, cmdArgs, stdout, stderr)
	case "xput":
		return runXPut(s, cmdArgs, stdout, stderr)
	case "chunk", "chunkstate":
		g, err := s.requireGeometry()
		if err != nil {
			return err
		}
		state := cmd == "chunkstate"
		request := fmt.Sprintf("CHUNKGET %s %s", cmdArgs[0], cmdArgs[1])
		if state {
			request += " STATE"
		}
		data, err := runBulk(client, request)
		if err != nil {
			return err
		}
		text, err := chunkText(data, g, state)
		if err != nil {
			return fmt.Errorf("%s failed: %w", cmd, err)
		}
		fmt.Fprintln(stdout, text)
	case "chunkset", "chunksetstate":
		g, err := s.requireGeometry()
		if err != nil {
			return err
		}
		data, err := chunkBytesFromText(cmdArgs[2], g, cmd == "chunksetstate")
		if err != nil {
			return fmt.Errorf("%s failed: %w", cmd, err)
		}
		version, err := putChunk(client, cmdArgs[0], cmdArgs[1], data, chunkPutArgs{state: cmd == "chunksetstate"})
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, version)
	case "chunkget":
		return runChunkGet(s, cmdArgs, stdout, stderr)
	case "chunkput":
		return runChunkPut(s, cmdArgs, stdout, stderr)
	case "chunkscan":
		items, err := runArray(client, "CHUNKSCAN "+strings.Join(cmdArgs, " "))
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Fprintln(stdout, string(item))
		}
	case "chunkrange", "chunkradius":
		g, err := s.requireGeometry()
		if err != nil {
			return err
		}
		items, err := runArray(client, strings.ToUpper(cmd)+" "+strings.Join(cmdArgs, " ")+" STATE")
		if err != nil {
			return err
		}
		if len(items)%2 != 0 {
			return fmt.Errorf("%s failed: odd number of reply items", cmd)
		}
		// One line per populated chunk: "<cx> <cy> <payload_bits>|<presence_bits>".
		for i := 0; i < len(items); i += 2 {
			text, err := chunkText(items[i+1], g, true)
			if err != nil {
				return fmt.Errorf("%s failed: chunk %s: %w", cmd, items[i], err)
			}
			fmt.Fprintf(stdout, "%s %s\n", items[i], text)
		}
	case "chunkbatch":
		batch, err := parseChunkBatchArgs(cmdArgs, stderr)
		if err != nil {
			return err
		}
		request := fmt.Sprintf("CHUNKBATCH %s %s", batch.cx, batch.cy)
		if batch.ifVersion != "" {
			request += " IF " + batch.ifVersion
		}
		payload, err := runBulk(client, request+" "+strings.Join(batch.ops, " "))
		if err != nil {
			return err
		}
		printTextPayload(stdout, payload)
	case "use":
		payload, err := runBulk(client, "USE "+cmdArgs[0])
		if err != nil {
			return err
		}
		if err := s.adopt(chunkclient.ParseInfo(payload)); err != nil {
			return err
		}
		s.named = true
		printTextPayload(stdout, payload)
	case "tables", "tableinfo", "tablecreate", "tableset", "tabledrop":
		return runTableCommand(client, cmd, cmdArgs, stdout)
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	return nil
}

// printBlock prints a block's bits, or "(unset)" for a block without a value.
func printBlock(out io.Writer, bits []byte) {
	if bits == nil {
		fmt.Fprintln(out, "(unset)")
		return
	}
	fmt.Fprintln(out, string(bits))
}

// runTableCommand runs one of the table commands and prints its reply: table
// names one per line, a table's key=value lines, or the status.
func runTableCommand(client *chunkclient.Client, cmd string, cmdArgs []string, stdout io.Writer) error {
	command := strings.ToUpper(cmd)
	if len(cmdArgs) > 0 {
		command += " " + strings.Join(cmdArgs, " ")
	}
	switch cmd {
	case "tables":
		items, err := runArray(client, command)
		if err != nil {
			return err
		}
		for _, item := range items {
			fmt.Fprintln(stdout, string(item))
		}
	case "tableinfo":
		payload, err := runBulk(client, command)
		if err != nil {
			return err
		}
		printTextPayload(stdout, payload)
	default:
		text, err := runSimple(client, command)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, text)
	}
	return nil
}

// chunkText renders chunk bytes as bit text: the payload bits, and with
// state "|" and the presence bits. Bit i is byte i/8, bit i%8 (LSB first).
func chunkText(data []byte, g geometry, state bool) (string, error) {
	want := g.payloadBytes()
	if state {
		want += g.presenceBytes()
	}
	if len(data) != want {
		return "", fmt.Errorf("got %d bytes, the table's chunks have %d", len(data), want)
	}
	text := bitsFromBytes(data[:g.payloadBytes()], g.payloadBits())
	if state {
		text += "|" + bitsFromBytes(data[g.payloadBytes():], g.blockCount())
	}
	return text, nil
}

// chunkBytesFromText parses "<payload_bits>" or, with state,
// "<payload_bits>|<presence_bits>" into the chunk bytes CHUNKPUT takes.
func chunkBytesFromText(text string, g geometry, state bool) ([]byte, error) {
	payloadBits, presenceBits := text, ""
	if state {
		payloadBits, presenceBits, _ = strings.Cut(text, "|")
	}
	if len(payloadBits) != g.payloadBits() {
		return nil, fmt.Errorf("the table's chunks have %d payload bits, got %d", g.payloadBits(), len(payloadBits))
	}
	data := bytesFromBits(payloadBits)
	if state {
		if len(presenceBits) != g.blockCount() {
			return nil, fmt.Errorf("the table's chunks have %d presence bits, got %d", g.blockCount(), len(presenceBits))
		}
		data = append(data, bytesFromBits(presenceBits)...)
	}
	return data, nil
}

func bitsFromBytes(data []byte, count int) string {
	var builder strings.Builder
	builder.Grow(count)
	for i := 0; i < count; i++ {
		if data[i/8]>>(i%8)&1 == 1 {
			builder.WriteByte('1')
		} else {
			builder.WriteByte('0')
		}
	}
	return builder.String()
}

func bytesFromBits(bits string) []byte {
	data := make([]byte, (len(bits)+7)/8)
	for i := 0; i < len(bits); i++ {
		if bits[i] == '1' {
			data[i/8] |= 1 << (i % 8)
		}
	}
	return data
}

type chunkPutArgs struct {
	state     bool
	extra     bool
	zrle      bool
	ifVersion string
}

// putChunk sends CHUNKPUT with the raw chunk bytes and returns the chunk's
// version after the write. With zrle the bytes go compressed when that is
// smaller.
func putChunk(client *chunkclient.Client, cx string, cy string, data []byte, args chunkPutArgs) (string, error) {
	request := fmt.Sprintf("CHUNKPUT %s %s", cx, cy)
	if args.state {
		request += " STATE"
	}
	if args.extra {
		request += " EXTRA"
	}
	body := data
	if args.zrle {
		if encoded := encodeZrle(data); len(encoded) < len(data) {
			body = encoded
			request += " ZRLE"
		}
	}
	if args.ifVersion != "" {
		request += " IF " + args.ifVersion
	}
	request += " " + strconv.Itoa(len(body))
	resp, err := client.CommandWithPayload(request, body)
	if err != nil {
		return "", fmt.Errorf("chunkput failed: %w", err)
	}
	if resp.Kind != chunkclient.ResponseBulk {
		return "", errors.New("chunkput failed: expected bulk response")
	}
	return string(resp.Bulk), nil
}

type chunkGetRequest struct {
	cx, cy string
	state  bool
	extra  bool
	zrle   bool
	out    string
}

func parseChunkGetArgs(cmdArgs []string, stderr io.Writer) (chunkGetRequest, error) {
	fs := flag.NewFlagSet("chunkget", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req chunkGetRequest
	fs.BoolVar(&req.state, "state", false, "include the presence bitmap")
	fs.BoolVar(&req.extra, "extra", false, "include the EXTRA section (implies --state)")
	fs.BoolVar(&req.zrle, "zrle", false, "transfer zrle-compressed")
	fs.StringVar(&req.out, "out", "", "write the bytes to file")
	if err := fs.Parse(protectNegativeArgs(cmdArgs, "out")); err != nil {
		return chunkGetRequest{}, err
	}
	remaining := fs.Args()
	if len(remaining) != 2 {
		return chunkGetRequest{}, errors.New("usage: chunkget [--state] [--extra] [--zrle] [--out <file>] <cx> <cy>")
	}
	req.state = req.state || req.extra
	req.cx, req.cy = remaining[0], remaining[1]
	if err := validateIntArg(req.cx, "cx"); err != nil {
		return chunkGetRequest{}, err
	}
	if err := validateIntArg(req.cy, "cy"); err != nil {
		return chunkGetRequest{}, err
	}
	return req, nil
}

// runChunkGet prints a chunk's raw bytes as a hex dump, or writes them to a
// file. With --zrle the transfer is compressed and decompressed here. With
// --extra the bytes end with the EXTRA section, whose values are listed after
// the dump.
func runChunkGet(s *session, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	req, err := parseChunkGetArgs(cmdArgs, stderr)
	if err != nil {
		return err
	}
	g, err := s.requireGeometry()
	if err != nil {
		return err
	}
	request := fmt.Sprintf("CHUNKGET %s %s", req.cx, req.cy)
	want := g.payloadBytes()
	if req.state {
		request += " STATE"
		want += g.presenceBytes()
	}
	// The EXTRA section follows the state; HELLO's max_extra_chunk_bytes
	// bounds it for every table.
	maxWant := want
	if req.extra {
		limit, err := s.extraCap()
		if err != nil {
			return err
		}
		request += " EXTRA"
		maxWant += limit
	}
	if req.zrle {
		request += " ZRLE"
	}
	body, err := runBulk(s.client, request)
	if err != nil {
		return err
	}
	data := body
	if req.zrle {
		if data, err = decodeZrleRange(body, want, maxWant); err != nil {
			return fmt.Errorf("chunkget failed: %w", err)
		}
	} else if req.extra && (len(data) < want || len(data) > maxWant) {
		return fmt.Errorf("chunkget failed: got %d bytes, the table's chunk state has %d and the EXTRA section at most %d",
			len(data), want, maxWant-want)
	} else if !req.extra && len(data) != want {
		return fmt.Errorf("chunkget failed: got %d bytes, the table's chunks have %d", len(data), want)
	}
	var values []extraValue
	if req.extra {
		if values, err = decodeExtraSection(data[want:], g.blockCount()); err != nil {
			return fmt.Errorf("chunkget failed: %w", err)
		}
	}

	if req.out != "" {
		if err := os.WriteFile(req.out, data, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", req.out, err)
		}
		fmt.Fprintf(stdout, "wrote %d bytes to %s\n", len(data), req.out)
		return nil
	}
	var extra []string
	if req.extra {
		if extra, err = extraLines(req.cx, req.cy, g, values); err != nil {
			return fmt.Errorf("chunkget failed: %w", err)
		}
	}
	if req.zrle {
		fmt.Fprintf(stdout, "bytes=%d compressed_bytes=%d\n", len(data), len(body))
	} else {
		fmt.Fprintf(stdout, "bytes=%d\n", len(data))
	}
	fmt.Fprint(stdout, hex.Dump(data))
	if req.extra {
		fmt.Fprintf(stdout, "extra_bytes=%d extra_values=%d\n", len(data)-want, len(extra))
		for _, line := range extra {
			fmt.Fprintln(stdout, line)
		}
	}
	return nil
}

type chunkPutRequest struct {
	cx, cy string
	args   chunkPutArgs
	// data is nil when it comes from --in and is read at run time.
	data   []byte
	inPath string
}

func parseChunkPutArgs(cmdArgs []string, stderr io.Writer) (chunkPutRequest, error) {
	fs := flag.NewFlagSet("chunkput", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req chunkPutRequest
	fs.BoolVar(&req.args.state, "state", false, "the bytes include the presence bitmap")
	fs.BoolVar(&req.args.extra, "extra", false, "the bytes end with an EXTRA section (implies --state)")
	fs.BoolVar(&req.args.zrle, "zrle", false, "send zrle-compressed when smaller")
	fs.StringVar(&req.args.ifVersion, "if", "", "write only if the chunk has this version")
	fs.StringVar(&req.inPath, "in", "", "read the bytes from file")
	if err := fs.Parse(protectNegativeArgs(cmdArgs, "if", "in")); err != nil {
		return chunkPutRequest{}, err
	}
	remaining := fs.Args()
	usage := errors.New("usage: chunkput [--state] [--extra] [--zrle] [--if <version>] <cx> <cy> <hex> | chunkput [flags] --in <file> <cx> <cy>")
	switch {
	case req.inPath != "" && len(remaining) == 2:
	case req.inPath == "" && len(remaining) == 3:
	default:
		return chunkPutRequest{}, usage
	}
	req.args.state = req.args.state || req.args.extra
	req.cx, req.cy = remaining[0], remaining[1]
	if err := validateIntArg(req.cx, "cx"); err != nil {
		return chunkPutRequest{}, err
	}
	if err := validateIntArg(req.cy, "cy"); err != nil {
		return chunkPutRequest{}, err
	}
	if req.args.ifVersion != "" {
		if _, err := strconv.ParseUint(req.args.ifVersion, 10, 64); err != nil {
			return chunkPutRequest{}, fmt.Errorf("invalid version %q: %w", req.args.ifVersion, err)
		}
	}
	if req.inPath == "" {
		data, err := hex.DecodeString(remaining[2])
		if err != nil {
			return chunkPutRequest{}, fmt.Errorf("payload must be hex: %w", err)
		}
		if len(data) == 0 {
			return chunkPutRequest{}, errors.New("payload must not be empty")
		}
		req.data = data
	}
	return req, nil
}

// runChunkPut writes a chunk's raw bytes, given as hex or read from a file,
// in the layout chunkget prints, and prints the chunk's version.
func runChunkPut(s *session, cmdArgs []string, stdout io.Writer, stderr io.Writer) error {
	req, err := parseChunkPutArgs(cmdArgs, stderr)
	if err != nil {
		return err
	}
	if req.inPath != "" {
		if req.data, err = os.ReadFile(req.inPath); err != nil {
			return fmt.Errorf("read %s: %w", req.inPath, err)
		}
	}
	g, err := s.requireGeometry()
	if err != nil {
		return err
	}
	want := g.payloadBytes()
	if req.args.state {
		want += g.presenceBytes()
	}
	if req.args.extra {
		limit, err := s.extraCap()
		if err != nil {
			return err
		}
		if len(req.data) < want {
			return fmt.Errorf("chunkput failed: got %d bytes, the table's chunk state has %d", len(req.data), want)
		}
		if len(req.data)-want > limit {
			return fmt.Errorf("chunkput failed: the EXTRA section has %d bytes, the server takes at most %d (max_extra_chunk_bytes)",
				len(req.data)-want, limit)
		}
		if _, err := decodeExtraSection(req.data[want:], g.blockCount()); err != nil {
			return fmt.Errorf("chunkput failed: %w", err)
		}
	} else if len(req.data) != want {
		return fmt.Errorf("chunkput failed: got %d bytes, the table's chunks have %d", len(req.data), want)
	}
	version, err := putChunk(s.client, req.cx, req.cy, req.data, req.args)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, version)
	return nil
}

type chunkBatchRequest struct {
	cx, cy    string
	ifVersion string
	ops       []string
}

func parseChunkBatchArgs(cmdArgs []string, stderr io.Writer) (chunkBatchRequest, error) {
	const usage = "usage: chunkbatch [--if <version>] <cx> <cy> SET <x> <y> <bits> | UNSET <x> <y> | XPUT <x> <y> <bits> | XDEL <x> <y> ..."
	fs := flag.NewFlagSet("chunkbatch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var req chunkBatchRequest
	fs.StringVar(&req.ifVersion, "if", "", "apply only if the chunk has this version")
	if err := fs.Parse(protectNegativeArgs(cmdArgs, "if")); err != nil {
		return chunkBatchRequest{}, err
	}
	args := fs.Args()
	if len(args) < 5 {
		return chunkBatchRequest{}, errors.New(usage)
	}
	req.cx, req.cy = args[0], args[1]
	if err := validateIntArg(req.cx, "cx"); err != nil {
		return chunkBatchRequest{}, err
	}
	if err := validateIntArg(req.cy, "cy"); err != nil {
		return chunkBatchRequest{}, err
	}
	if req.ifVersion != "" {
		if _, err := strconv.ParseUint(req.ifVersion, 10, 64); err != nil {
			return chunkBatchRequest{}, fmt.Errorf("invalid version %q: %w", req.ifVersion, err)
		}
	}
	i := 2
	for i < len(args) {
		switch strings.ToLower(args[i]) {
		case "set", "xput":
			if i+3 > len(args)-1 {
				return chunkBatchRequest{}, errors.New(usage)
			}
			if err := validateIntArg(args[i+1], "x"); err != nil {
				return chunkBatchRequest{}, err
			}
			if err := validateIntArg(args[i+2], "y"); err != nil {
				return chunkBatchRequest{}, err
			}
			if err := validateBits(args[i+3]); err != nil {
				return chunkBatchRequest{}, err
			}
			i += 4
		case "unset", "xdel":
			if i+2 > len(args)-1 {
				return chunkBatchRequest{}, errors.New(usage)
			}
			if err := validateIntArg(args[i+1], "x"); err != nil {
				return chunkBatchRequest{}, err
			}
			if err := validateIntArg(args[i+2], "y"); err != nil {
				return chunkBatchRequest{}, err
			}
			i += 3
		default:
			return chunkBatchRequest{}, fmt.Errorf("batch operations must be SET, UNSET, XPUT or XDEL, got %q", args[i])
		}
	}
	req.ops = args[2:]
	return req, nil
}

func validateCommandArgs(cmd string, cmdArgs []string) error {
	intArgs := func(usage string, fields ...string) error {
		if len(cmdArgs) != len(fields) {
			return errors.New(usage)
		}
		for i, field := range fields {
			if err := validateIntArg(cmdArgs[i], field); err != nil {
				return err
			}
		}
		return nil
	}
	switch cmd {
	case "ping", "info", "walflush", "metrics", "tables", "shell":
		if len(cmdArgs) != 0 {
			return fmt.Errorf("usage: %s", cmd)
		}
	case "get":
		return intArgs("usage: get <x> <y>", "x", "y")
	case "unset":
		return intArgs("usage: unset <x> <y>", "x", "y")
	case "xdel":
		return intArgs("usage: xdel <x> <y>", "x", "y")
	case "xget":
		_, err := parseXGetArgs(cmdArgs, io.Discard)
		return err
	case "xput":
		_, err := parseXPutArgs(cmdArgs, io.Discard)
		return err
	case "chunkexists", "chunk", "chunkstate", "chunkver":
		return intArgs(fmt.Sprintf("usage: %s <cx> <cy>", cmd), "cx", "cy")
	case "set":
		if len(cmdArgs) != 3 {
			return errors.New("usage: set <x> <y> <bits>")
		}
		if err := intArgsPrefix(cmdArgs, "x", "y"); err != nil {
			return err
		}
		return validateNonEmptyBits(cmdArgs[2])
	case "mset":
		if len(cmdArgs) == 0 || len(cmdArgs)%3 != 0 {
			return errors.New("usage: mset <x> <y> <bits> [<x> <y> <bits> ...]")
		}
		for i := 0; i < len(cmdArgs); i += 3 {
			if err := intArgsPrefix(cmdArgs[i:], "x", "y"); err != nil {
				return err
			}
			if err := validateNonEmptyBits(cmdArgs[i+2]); err != nil {
				return err
			}
		}
	case "mget":
		if len(cmdArgs) == 0 || len(cmdArgs)%2 != 0 {
			return errors.New("usage: mget <x> <y> [<x> <y> ...]")
		}
		for i := 0; i < len(cmdArgs); i += 2 {
			if err := intArgsPrefix(cmdArgs[i:], "x", "y"); err != nil {
				return err
			}
		}
	case "chunkset":
		if len(cmdArgs) != 3 {
			return errors.New("usage: chunkset <cx> <cy> <bits>")
		}
		if err := intArgsPrefix(cmdArgs, "cx", "cy"); err != nil {
			return err
		}
		return validateNonEmptyBits(cmdArgs[2])
	case "chunksetstate":
		if len(cmdArgs) != 3 {
			return errors.New("usage: chunksetstate <cx> <cy> <payload_bits>|<presence_bits>")
		}
		if err := intArgsPrefix(cmdArgs, "cx", "cy"); err != nil {
			return err
		}
		return validateChunkState(cmdArgs[2])
	case "chunkget":
		_, err := parseChunkGetArgs(cmdArgs, io.Discard)
		return err
	case "chunkput":
		_, err := parseChunkPutArgs(cmdArgs, io.Discard)
		return err
	case "chunkbatch":
		_, err := parseChunkBatchArgs(cmdArgs, io.Discard)
		return err
	case "chunkradius":
		if len(cmdArgs) != 3 {
			return errors.New("usage: chunkradius <cx> <cy> <radius_chunks>")
		}
		if err := intArgsPrefix(cmdArgs, "cx", "cy"); err != nil {
			return err
		}
		if _, err := strconv.ParseUint(cmdArgs[2], 10, 64); err != nil {
			return fmt.Errorf("invalid radius_chunks %q: %w", cmdArgs[2], err)
		}
	case "chunkscan":
		if len(cmdArgs) != 1 && len(cmdArgs) != 3 {
			return errors.New("usage: chunkscan <limit> [<cursor_cx> <cursor_cy>]")
		}
		if _, err := strconv.ParseUint(cmdArgs[0], 10, 64); err != nil {
			return fmt.Errorf("invalid limit %q: %w", cmdArgs[0], err)
		}
		if len(cmdArgs) == 3 {
			return intArgsPrefix(cmdArgs[1:], "cursor_cx", "cursor_cy")
		}
	case "chunkrange":
		return intArgs("usage: chunkrange <cx0> <cy0> <cx1> <cy1>", "cx0", "cy0", "cx1", "cy1")
	case "tableinfo", "use", "tabledrop":
		if len(cmdArgs) != 1 {
			return fmt.Errorf("usage: %s <table>", cmd)
		}
	case "tablecreate":
		if len(cmdArgs) < 3 || len(cmdArgs)%2 != 1 {
			return errors.New("usage: tablecreate <table> block_bits <n> [<key> <value> ...]")
		}
	case "tableset":
		if len(cmdArgs) < 3 || len(cmdArgs)%2 != 1 {
			return errors.New("usage: tableset <table> <option> <value> [<option> <value> ...]")
		}
	}
	return nil
}

// intArgsPrefix checks that the first len(fields) arguments are integers.
func intArgsPrefix(args []string, fields ...string) error {
	for i, field := range fields {
		if err := validateIntArg(args[i], field); err != nil {
			return err
		}
	}
	return nil
}

func validateNonEmptyBits(bits string) error {
	if bits == "" {
		return errors.New("bits must not be empty")
	}
	return validateBits(bits)
}

// validateIntArg accepts what the server parses as an integer: an optional
// '-' and decimal digits. strconv also takes a leading '+', which the server
// refuses; in a payload header that closes the connection.
func validateIntArg(value string, field string) error {
	digits := strings.TrimPrefix(value, "-")
	if digits == "" || strings.Trim(digits, "0123456789") != "" {
		return fmt.Errorf("invalid %s %q: not an integer", field, value)
	}
	if _, err := strconv.ParseInt(value, 10, 64); err != nil {
		return fmt.Errorf("invalid %s %q: %w", field, value, err)
	}
	return nil
}

func validateChunkState(state string) error {
	if state == "" {
		return errors.New("chunk state must not be empty")
	}
	separator := strings.Index(state, "|")
	if separator <= 0 || separator != strings.LastIndex(state, "|") || separator == len(state)-1 {
		return errors.New("chunk state must be <payload_bits>|<presence_bits>")
	}
	if err := validateBits(state[:separator]); err != nil {
		return err
	}
	return validateBits(state[separator+1:])
}

func runShell(s *session, input io.Reader, stdout io.Writer, stderr io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 0, 4096), 1024*1024)
	for {
		// The prompt names the table once one was named.
		prompt := "chunk> "
		if s.named && s.table != "" {
			prompt = "chunk:" + s.table + "> "
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
		if line == "" {
			continue
		}

		fields := strings.Fields(line)
		cmd := strings.ToLower(fields[0])
		cmdArgs := fields[1:]

		switch {
		case cmd == "exit":
			return nil
		case cmd == "quit":
			text, err := runSimple(s.client, "QUIT")
			if err != nil {
				fmt.Fprintf(stderr, "error: %v\n", err)
				return nil
			}
			fmt.Fprintln(stdout, text)
			return nil
		case cmd == "shell" || !networkCommands[cmd]:
			fmt.Fprintf(stderr, "error: unknown shell command %q\n", cmd)
			continue
		}
		if err := validateCommandArgs(cmd, cmdArgs); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			continue
		}
		if err := runCommand(s, cmd, cmdArgs, stdout, stderr); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
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

// runBulkOrNull returns a bulk reply, or nil for `$-1`.
func runBulkOrNull(client *chunkclient.Client, command string) ([]byte, error) {
	resp, err := client.Command(command)
	if err != nil {
		return nil, fmt.Errorf("%s failed: %w", commandVerb(command), err)
	}
	switch resp.Kind {
	case chunkclient.ResponseNull:
		return nil, nil
	case chunkclient.ResponseBulk:
		return resp.Bulk, nil
	default:
		return nil, fmt.Errorf("%s failed: expected bulk response", commandVerb(command))
	}
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
	fs.StringVar(&opts.Table, "table", "", "table to work on (preferred over the table in the URI path)")
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
  get <x> <y>                      prints the bits, or (unset)
  set <x> <y> <bits>
  unset <x> <y>
  mset <x> <y> <bits> [<x> <y> <bits> ...]
  mget <x> <y> [<x> <y> ...]
  xget [--bits] <x> <y>            extra data: bit length and hex, or (none)
  xput <x> <y> <bits>
  xput --hex --bit-length <n> <x> <y> <hex>
  xput --bit-length <n> --in <file> <x> <y>
  xdel <x> <y>
  chunkexists <cx> <cy>
  chunk <cx> <cy>                  payload as bit text
  chunkstate <cx> <cy>             <payload_bits>|<presence_bits>
  chunkset <cx> <cy> <bits>
  chunksetstate <cx> <cy> <payload_bits>|<presence_bits>
  chunkget [--state] [--extra] [--zrle] [--out <file>] <cx> <cy>
  chunkput [--state] [--extra] [--zrle] [--if <version>] <cx> <cy> <hex>
  chunkput [--state] [--extra] [--zrle] [--if <version>] --in <file> <cx> <cy>
  chunkscan <limit> [<cursor_cx> <cursor_cy>]
  chunkrange <cx0> <cy0> <cx1> <cy1>
  chunkradius <cx> <cy> <radius_chunks>
  chunkver <cx> <cy>
  chunkbatch [--if <version>] <cx> <cy> SET <x> <y> <bits> | UNSET <x> <y> | XPUT <x> <y> <bits> | XDEL <x> <y> ...
  walflush
  metrics
  tables
  tableinfo <table>
  use <table>
  tablecreate <table> block_bits <n> [<key> <value> ...]
  tableset <table> <option> <value> [<option> <value> ...]
  tabledrop <table>
  shell
  version
  help

Global options:
  --uri <chunk://token@host:port/ | chunks://token@host:port/>
  --token <token>
  --table <table>          default: the URI path (chunk://host:port/<table>), else "default"
  --timeout <duration>
  --tls-insecure
  --tls-server-name <name>

Examples:
  chunk-cli --uri chunk://token@127.0.0.1:4242/ ping
  chunk-cli --uri chunk://token@127.0.0.1:4242/ get 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunkstate 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunkget --state --out chunk.bin 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ chunkput --state --if 42 --in chunk.bin 0 0
  chunk-cli --uri chunk://token@127.0.0.1:4242/ tablecreate terrain block_bits 4 chunk_width_blocks 32
  chunk-cli --uri chunk://token@127.0.0.1:4242/terrain get 0 0
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
