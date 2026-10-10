package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// statementOptions are the options of one statement: given as global flags
// for a one-shot statement, or before the statement on a shell line.
type statementOptions struct {
	json   bool
	blocks bool
	// in is a file sent as the parameter $1 (a chunk form for SET CHUNK).
	in string
	// out is a file the reply's bytes are written to (a chunk form of GET
	// CHUNK).
	out string
	// newPasswordFile holds the password of CREATE USER / ALTER USER ...
	// PASSWORD.
	newPasswordFile string
}

// statementKind is what the CLI needs to know of a statement to print its
// reply.
type statementKind int

const (
	statementOther statementKind = iota
	statementGetBlock
	statementGetChunk
	statementGetArea
	statementDescribe
)

type statementInfo struct {
	kind  statementKind
	table string
	// columns are the names after COLUMNS.
	columns []string
	// cx, cy are the chunk of GET CHUNK.
	cx, cy int64
}

// tokenize splits a statement into words, skipping quoted values and the
// punctuation `,`, `(`, `)`, `=`; it is only used to recognise the reads
// whose replies are decoded with the table's schema.
func tokenize(statement string) []string {
	var tokens []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, current.String())
			current.Reset()
		}
	}
	quoted := false
	for i := 0; i < len(statement); i++ {
		c := statement[i]
		switch {
		case c == '\'':
			quoted = !quoted
			flush()
		case quoted:
		case c == ' ' || c == '\t' || c == ',' || c == '(' || c == ')' || c == '=':
			flush()
		default:
			current.WriteByte(c)
		}
	}
	flush()
	return tokens
}

func classify(statement string) statementInfo {
	tokens := tokenize(statement)
	keyword := func(i int) string {
		if i < len(tokens) {
			return strings.ToUpper(tokens[i])
		}
		return ""
	}
	if keyword(0) == "DESCRIBE" && len(tokens) == 2 {
		return statementInfo{kind: statementDescribe, table: tokens[1]}
	}
	if keyword(0) != "GET" {
		return statementInfo{}
	}
	info := statementInfo{}
	switch keyword(1) {
	case "BLOCK":
		info.kind = statementGetBlock
	case "CHUNK":
		info.kind = statementGetChunk
		var errX, errY error
		info.cx, errX = strconv.ParseInt(keyword(2), 10, 64)
		info.cy, errY = strconv.ParseInt(keyword(3), 10, 64)
		if errX != nil || errY != nil {
			return statementInfo{}
		}
	case "AREA":
		info.kind = statementGetArea
	default:
		return statementInfo{}
	}
	from := -1
	for i := 2; i < len(tokens); i++ {
		if keyword(i) == "FROM" {
			from = i
			break
		}
	}
	if from < 0 || from+1 >= len(tokens) {
		return statementInfo{}
	}
	info.table = tokens[from+1]
	if from+2 < len(tokens) {
		if keyword(from+2) != "COLUMNS" || from+3 >= len(tokens) {
			return statementInfo{}
		}
		info.columns = tokens[from+3:]
	}
	return info
}

// parseLineOptions takes the options at the start of a shell line
// (`--json`, `--blocks`, `--in <file>`, `--out <file>`,
// `--new-password-file <file>`) and returns them with the statement that
// follows, unchanged.
func parseLineOptions(line string, defaults statementOptions) (statementOptions, string, error) {
	opts := defaults
	rest := strings.TrimSpace(line)
	for strings.HasPrefix(rest, "--") {
		word, after, _ := strings.Cut(rest, " ")
		after = strings.TrimSpace(after)
		switch word {
		case "--json":
			opts.json = true
		case "--blocks":
			opts.blocks = true
		case "--in", "--out", "--new-password-file":
			file, remaining, _ := strings.Cut(after, " ")
			if file == "" {
				return opts, "", fmt.Errorf("%s needs a file", word)
			}
			switch word {
			case "--in":
				opts.in = file
			case "--out":
				opts.out = file
			default:
				opts.newPasswordFile = file
			}
			after = strings.TrimSpace(remaining)
		default:
			return opts, "", fmt.Errorf("unknown option %q", word)
		}
		rest = after
	}
	if rest == "" {
		return opts, "", errors.New("no statement after the options")
	}
	return opts, rest, nil
}

// execute sends one statement and prints its reply. txn is the shell's
// transaction state, nil for a one-shot statement.
func execute(client *chunkclient.Client, txn *shellTxn, statement string, opts statementOptions, stdout io.Writer, term console) (err error) {
	tokens := strings.Fields(statement)
	if len(tokens) > 0 && strings.EqualFold(tokens[0], "WATCH") {
		return errors.New("WATCH streams use chunk-cli watch <table>, outside the shell")
	}
	inTxn := txn != nil && txn.open
	if txn != nil {
		defer func() { err = txn.ended(client, statement, err) }()
		if control := txnControlOf(statement); control != txnNone {
			return txn.control(client, control, statement, opts, stdout)
		}
	}

	var parameters [][]byte
	if rewritten, user, ok := passwordStatement(statement); ok {
		if opts.in != "" {
			return errors.New("--in cannot be used with PASSWORD, whose verifier is the parameter $1")
		}
		password, err := newPassword(opts, user, term)
		if err != nil {
			return err
		}
		verifier, err := chunkclient.Verifier(password, chunkclient.MinIterations)
		if err != nil {
			return err
		}
		statement, parameters = rewritten, [][]byte{[]byte(verifier)}
	} else if opts.in != "" {
		data, err := os.ReadFile(opts.in)
		if err != nil {
			return fmt.Errorf("read %s: %w", opts.in, err)
		}
		parameters = [][]byte{data}
	}

	if opts.out != "" {
		reply, err := client.Do(statement, parameters...)
		if err != nil {
			return err
		}
		if reply.Kind != chunkclient.KindBulk {
			return fmt.Errorf("--out needs a reply of bytes (such as GET CHUNK), got a %s", reply.Kind)
		}
		if err := os.WriteFile(opts.out, reply.Bulk, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", opts.out, err)
		}
		if opts.json {
			return writeJSON(stdout, orderedMap{{"file", opts.out}, {"bytes", len(reply.Bulk)}})
		}
		_, err = fmt.Fprintf(stdout, "wrote %d bytes to %s\n", len(reply.Bulk), opts.out)
		return err
	}

	info := classify(statement)
	if info.kind == statementOther || info.kind == statementDescribe {
		reply, err := client.Do(statement, parameters...)
		if err != nil {
			return err
		}
		if info.kind == statementDescribe {
			schema, err := chunkclient.ParseDescribe(reply)
			if err != nil {
				return err
			}
			if !opts.json {
				return printSchema(stdout, schema)
			}
		}
		// A write inside a transaction answers `_`: it applies at COMMIT.
		if inTxn && reply.Kind == chunkclient.KindNull && !opts.json && isWrite(statement) {
			_, err = fmt.Fprintln(stdout, "(applies at COMMIT)")
			return err
		}
		return printReply(stdout, reply, opts)
	}

	// A read whose reply is decoded by the table's columns: the schema is
	// read in the same round trip, just before the statement (also inside a
	// transaction, which runs DESCRIBE).
	replies, err := client.Pipeline([]chunkclient.Request{
		{Statement: "DESCRIBE " + info.table},
		{Statement: statement, Parameters: parameters},
	})
	if err != nil {
		return err
	}
	if replies[1].Err != nil {
		return replies[1].Err
	}
	if replies[0].Err != nil {
		return fmt.Errorf("DESCRIBE %s: %w", info.table, replies[0].Err)
	}
	schema, err := chunkclient.ParseDescribe(replies[0].Value)
	if err != nil {
		return err
	}
	reply := replies[1].Value
	switch info.kind {
	case statementGetBlock:
		return printBlock(stdout, schema, info.columns, reply, opts)
	case statementGetChunk:
		if reply.Kind != chunkclient.KindBulk {
			return fmt.Errorf("GET CHUNK: expected a chunk form, got a %s", reply.Kind)
		}
		chunk, err := chunkclient.DecodeChunkForm(schema, info.columns, reply.Bulk)
		if err != nil {
			return err
		}
		return printChunk(stdout, chunk, info.cx, info.cy, opts)
	default:
		return printArea(stdout, schema, info.columns, reply, opts)
	}
}
