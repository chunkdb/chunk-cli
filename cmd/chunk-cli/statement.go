package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

// statementOptions are the options of one statement: given as global flags
// for a one-shot statement, or before the statement on a shell line.
type statementOptions struct {
	areaCheck *areaResponseDeadline
	json      bool
	blocks    bool
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
	statementScanChunks
)

type statementInfo struct {
	kind  statementKind
	table string
	// columns are the names after COLUMNS.
	columns                 []string
	withVersion, withSchema bool
	// Coordinates retain explicit request arity, including a third zero axis.
	cx, cy, cz int64
	dims       int
	scanSince  *uint64
	areaPage   *areaPageRequest
}

// tokenize splits a statement into words, skipping quoted values and the
// punctuation `,`, `(`, `)`, `=`; it is only used to recognise the reads
// whose replies are decoded with the table's schema.
func tokenize(statement string, keepCommas ...bool) []string {
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
		case !quoted && c == '"':
			flush()
			start := i
			for i++; i < len(statement); i++ {
				if statement[i] == '"' {
					if i+1 < len(statement) && statement[i+1] == '"' {
						i++
						continue
					}
					break
				}
			}
			if i == len(statement) {
				return nil
			}
			tokens = append(tokens, statement[start:i+1])
		case quoted:
		case c == ',' && len(keepCommas) != 0:
			flush()
			tokens = append(tokens, ",")
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
	tokens := tokenize(statement, true)
	if isAreaReadStatement(statement) {
		tokens = rawChunkTokens(statement, true)
	}
	keyword := func(i int) string {
		if i < len(tokens) {
			return strings.ToUpper(tokens[i])
		}
		return ""
	}
	if keyword(0) == "DESCRIBE" && len(tokens) == 2 {
		return statementInfo{kind: statementDescribe, table: tokens[1]}
	}
	if keyword(0) == "SCAN" && keyword(1) == "CHUNKS" && keyword(2) == "FROM" && len(tokens) >= 6 {
		info := statementInfo{kind: statementScanChunks, table: tokens[3]}
		for i := 4; i < len(tokens); {
			switch keyword(i) {
			case "AFTER":
				i++
				start := i
				for i < len(tokens) {
					if _, err := strconv.ParseInt(tokens[i], 10, 64); err != nil {
						break
					}
					i++
				}
				if i-start != 2 && i-start != 3 {
					return statementInfo{}
				}
			case "SINCE", "LIMIT":
				if i+1 >= len(tokens) {
					return statementInfo{}
				}
				value, err := strconv.ParseUint(strings.TrimPrefix(tokens[i+1], "-"), 10, 64)
				if err != nil || strings.HasPrefix(tokens[i+1], "-") && value != 0 {
					return statementInfo{}
				}
				if keyword(i) == "SINCE" {
					info.scanSince = &value
				}
				i += 2
			case "WITH":
				if keyword(i+1) != "DATA" {
					return statementInfo{}
				}
				i += 2
				if i != len(tokens) {
					if keyword(i) != "COLUMNS" || i+1 >= len(tokens) {
						return statementInfo{}
					}
					var ok bool
					info.columns, ok = readColumnNames(tokens[i+1:])
					if !ok {
						return statementInfo{}
					}
				}
				return info
			default:
				return statementInfo{}
			}
		}
		return statementInfo{}
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
	if info.kind == statementGetBlock || info.kind == statementGetChunk {
		info.dims = from - 2
		if info.dims != 2 && info.dims != 3 {
			return statementInfo{}
		}
		axes := []*int64{&info.cx, &info.cy, &info.cz}
		for axis := range info.dims {
			value, err := strconv.ParseInt(tokens[axis+2], 10, 64)
			if err != nil {
				return statementInfo{}
			}
			*axes[axis] = value
		}
	}
	info.table = tokens[from+1]
	var areaSelector *areaPageRequest
	if info.kind == statementGetArea {
		areaSelector = parseAreaSelector(tokens[2:from])
	}
	for i := from + 2; i < len(tokens); {
		switch keyword(i) {
		case "COLUMNS":
			start := i + 1
			i = start
			for i < len(tokens) {
				if info.kind == statementGetArea && i > start && tokens[i-1] != "," && (keyword(i) == "LIMIT" || keyword(i) == "AFTER") {
					break
				}
				if i > start && tokens[i-1] != "," && keyword(i) == "WITH" && (keyword(i+1) == "VERSION" || keyword(i+1) == "SCHEMA") {
					break
				}
				i++
			}
			if i == start || info.columns != nil {
				return statementInfo{}
			}
			var ok bool
			info.columns, ok = readColumnNames(tokens[start:i])
			if !ok {
				return statementInfo{}
			}
		case "WITH":
			if keyword(i+1) == "VERSION" && info.kind == statementGetBlock && !info.withVersion {
				info.withVersion = true
			} else if keyword(i+1) == "SCHEMA" && info.kind != statementGetChunk && !info.withSchema {
				info.withSchema = true
			} else {
				return statementInfo{}
			}
			i += 2
		case "LIMIT":
			if info.kind != statementGetArea || areaSelector == nil || areaSelector.limit != 0 || i+1 >= len(tokens) {
				return statementInfo{}
			}
			limit, err := strconv.ParseUint(tokens[i+1], 10, 64)
			if err != nil || limit == 0 {
				return statementInfo{}
			}
			areaSelector.limit = limit
			info.areaPage = areaSelector
			i += 2
		case "AFTER":
			if info.kind != statementGetArea || areaSelector == nil || areaSelector.after != nil || i+areaSelector.dims >= len(tokens) {
				return statementInfo{}
			}
			point := &[3]int64{}
			for axis := range areaSelector.dims {
				value, err := strconv.ParseInt(tokens[i+axis+1], 10, 64)
				if err != nil {
					return statementInfo{}
				}
				point[axis] = value
			}
			areaSelector.after = point
			info.areaPage = areaSelector
			i += areaSelector.dims + 1
		default:
			return statementInfo{}
		}
	}
	return info
}

func readColumnNames(tokens []string) ([]string, bool) {
	if len(tokens) == 0 || len(tokens)%2 == 0 {
		return nil, false
	}
	names := make([]string, 0, (len(tokens)+1)/2)
	for i, name := range tokens {
		if i%2 != 0 {
			if name != "," {
				return nil, false
			}
			continue
		}
		if name == "," {
			return nil, false
		}
		if strings.HasPrefix(name, "\"") {
			name = strings.ReplaceAll(name[1:len(name)-1], "\"\"", "\"")
		}
		names = append(names, name)
	}
	return names, true
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
		var data []byte
		var err error
		if shape, ok := rawChunkShapeOf(statement); ok {
			data, err = readChunkImport(client, shape.table, opts.in, shape.columns...)
		} else {
			data, err = os.ReadFile(opts.in)
		}
		if err != nil {
			return fmt.Errorf("read %s: %w", opts.in, err)
		}
		parameters = [][]byte{data}
	}

	info := classify(statement)
	defer func() {
		var malformed *areaReplyError
		if errors.As(err, &malformed) {
			client.RejectReply(err)
			if txn != nil {
				txn.open = false
			}
		}
	}()
	if info.areaPage != nil {
		server := client.Info()
		if server == nil || !server.Supports("area_paging") {
			return errors.New("GET AREA: server does not support area_paging")
		}
		info.areaPage.maxChunks = server.MaxAreaChunks
	}
	readLimits, err := areaReadLimits(client.Info(), statement, info)
	if err != nil {
		return err
	}
	if opts.out != "" {
		reply, err := limitedStatement(client, statement, readLimits, parameters)
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

	if info.kind == statementOther || info.kind == statementDescribe {
		reply, err := limitedStatement(client, statement, readLimits, parameters)
		if err != nil {
			return err
		}
		words := strings.Fields(statement)
		if len(words) >= 2 && strings.EqualFold(words[0], "SHOW") {
			if strings.EqualFold(words[1], "SLOTS") {
				err = chunkclient.ValidateSlots(reply)
			}
			if strings.EqualFold(words[1], "STATUS") {
				err = chunkclient.ValidateStatus(reply)
			}
			if err != nil {
				return err
			}
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
		{Statement: statement, Parameters: parameters, ReplyLimits: readLimits},
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
	if info.kind == statementGetArea {
		opts.areaCheck = &areaResponseDeadline{deadline: replies[1].Deadline, now: time.Now}
		if err := opts.areaCheckpoint(); err != nil {
			return err
		}
	}
	schema, err := chunkclient.ParseDescribe(replies[0].Value)
	if info.kind == statementGetArea {
		if expired := opts.areaCheckpoint(); expired != nil {
			return expired
		}
	}
	if err != nil {
		if info.kind == statementGetArea {
			return malformedAreaReply(err)
		}
		return err
	}
	reply := replies[1].Value
	if info.kind == statementGetArea {
		return printAreaResponse(stdout, schema, info, replies[1], opts)
	}
	if info.withVersion || info.withSchema {
		return printReadMetadata(stdout, schema, info, reply, opts)
	}
	switch info.kind {
	case statementScanChunks:
		return printChunkScan(stdout, schema, info, reply, opts)
	case statementGetBlock:
		return printBlock(stdout, schema, info.columns, reply, opts)
	case statementGetChunk:
		if reply.Kind == chunkclient.KindNull {
			if opts.json {
				return writeJSON(stdout, nil)
			}
			_, err := fmt.Fprintln(stdout, "(null)")
			return err
		}
		if reply.Kind != chunkclient.KindBulk {
			return fmt.Errorf("GET CHUNK: expected a chunk form, got a %s", reply.Kind)
		}
		chunk, err := decodeOutputChunk(schema, info.columns, reply.Bulk, opts.blocks)
		if err != nil {
			return err
		}
		if info.dims == 3 {
			return printChunk(stdout, chunk, info.cx, info.cy, opts, info.cz)
		}
		return printChunk(stdout, chunk, info.cx, info.cy, opts)
	default:
		if info.areaPage != nil {
			return printAreaPage(stdout, schema, info, reply, opts)
		}
		return printArea(stdout, schema, info.columns, reply, opts)
	}
}
