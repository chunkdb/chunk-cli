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
	// inMore holds the files of the parameters $2 and up, by number.
	inMore map[int]string
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
	statementScanIndex
	statementScanChunks
	// statementReturning is SET BLOCK or DELETE BLOCK with RETURNING.
	statementReturning
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
	scanAfter  *[3]int64
	// scanWhere reports a SCAN CHUNKS WHERE, whose reply carries after.
	scanWhere bool
	areaPage  *areaPageRequest
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
	if (keyword(0) == "SET" || keyword(0) == "DELETE") && keyword(1) == "BLOCK" {
		for _, token := range tokens[2:] {
			for at := 0; at+len("RETURNING") <= len(token); at++ {
				if strings.EqualFold(token[at:at+len("RETURNING")], "RETURNING") {
					return classifyReturning(replyTokens(statement))
				}
			}
		}
		return statementInfo{}
	}
	if keyword(0) == "DESCRIBE" && len(tokens) == 2 {
		return statementInfo{kind: statementDescribe, table: tokens[1]}
	}
	if keyword(0) == "SCAN" && keyword(1) == "CHUNKS" {
		return classifyScan(statement)
	}
	if keyword(0) != "GET" {
		return statementInfo{}
	}
	info := classifyGet(tokens)
	if info.kind == statementOther && (keyword(1) == "BLOCK" || keyword(1) == "CHUNK") {
		// The server lexer also accepts adjacent coordinates and keywords.
		return classifyGet(rawChunkTokens(statement, true))
	}
	return info
}

func classifyGet(tokens []string) statementInfo {
	keyword := func(i int) string {
		if i < len(tokens) {
			return strings.ToUpper(tokens[i])
		}
		return ""
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
				if info.kind == statementGetArea && i > start && tokens[i-1] != "," && (keyword(i) == "LIMIT" || keyword(i) == "AFTER" || keyword(i) == "SCALE") {
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
		case "SCALE":
			// SCALE k USING reduction: the forms carry their scale in section 7.
			if info.kind != statementGetArea || i+3 >= len(tokens) || keyword(i+2) != "USING" {
				return statementInfo{}
			}
			if _, err := strconv.ParseUint(tokens[i+1], 10, 32); err != nil {
				return statementInfo{}
			}
			i += 4
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
	lineIn := false
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
				if !lineIn {
					// A line's own --in replaces the global ones.
					opts.in, opts.inMore, lineIn = "", nil, true
				}
				if err := opts.addIn(file); err != nil {
					return opts, "", err
				}
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
		return errors.New("WATCH streams use chunk-cli watch <table> or chunk-cli watch --catalog, outside the shell")
	}
	inTxn := txn != nil && txn.open
	if txn != nil {
		defer func() { err = txn.ended(client, statement, err) }()
		if control := txnControlOf(statement); control != txnNone {
			return txn.control(client, control, statement, opts, stdout)
		}
	}

	var parameters [][]byte
	if rewritten, user, add, ok := passwordStatement(statement); ok {
		if opts.hasIn() {
			return errors.New("--in cannot be used with PASSWORD, whose verifier is the parameter $1")
		}
		info := client.Info()
		if add && (info == nil || info.LoginUser != user || len(info.LoginSalt) == 0) {
			return fmt.Errorf("ADD PASSWORD reuses the salt of %s's current verifier, which only a login as %s receives: connect as %s, or run ALTER USER %s ADD VERIFIER with a verifier computed elsewhere", user, user, user, user)
		}
		password, err := newPassword(opts, user, term)
		if err != nil {
			return err
		}
		verifier, err := passwordVerifier(password, add, info)
		if err != nil {
			return err
		}
		statement, parameters = rewritten, [][]byte{[]byte(verifier)}
	} else if opts.hasIn() {
		files, err := opts.inFiles()
		if err != nil {
			return err
		}
		for i, file := range files {
			var data []byte
			if shape, ok := rawChunkShapeOf(statement); ok && i == 0 {
				data, err = readChunkImport(client, shape.table, file, shape.columns...)
			} else {
				data, err = os.ReadFile(file)
			}
			if err != nil {
				return fmt.Errorf("read %s: %w", file, err)
			}
			parameters = append(parameters, data)
		}
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
		if err := validateStatementReply(statement, tokens, info, reply, inTxn); err != nil {
			return err
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

	if info.kind == statementOther || info.kind == statementDescribe || info.kind == statementScanIndex {
		reply, err := limitedStatement(client, statement, readLimits, parameters)
		if err != nil {
			return err
		}
		if err := validateStatementReply(statement, tokens, info, reply, inTxn); err != nil {
			return err
		}
		if info.kind == statementDescribe {
			schema, err := chunkclient.ParseDescribe(reply)
			if err != nil {
				return err
			}
			if err := validateSchemaOutput("DESCRIBE", reply, schema); err != nil {
				return err
			}
			if opts.json {
				return writeJSON(stdout, schemaJSON(reply, schema))
			}
			return printSchema(stdout, schema)
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
	case statementReturning:
		return printReturning(stdout, schema, info, reply, opts)
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

// hasIn reports whether any --in file was given.
// maxInParameter is the largest parameter number the server accepts.
const maxInParameter = 65535

func (o statementOptions) hasIn() bool { return o.in != "" || len(o.inMore) > 0 }

// addIn records an --in value: a file for the parameter $1, or n=file for the
// parameter $n.
func (o *statementOptions) addIn(spec string) error {
	number, file := 1, spec
	if digits, rest, found := strings.Cut(spec, "="); found {
		if n, err := strconv.Atoi(digits); err == nil {
			if n < 1 {
				return fmt.Errorf("--in %s: parameters are numbered from $1", spec)
			}
			if n > maxInParameter {
				return fmt.Errorf("--in %s: parameters are numbered up to $%d; write ./%s for a file name that starts with digits and =", spec, maxInParameter, spec)
			}
			number, file = n, rest
		}
	}
	if file == "" {
		return errors.New("--in needs a file")
	}
	if number == 1 {
		if o.in != "" {
			return errors.New("--in gives parameter $1 twice")
		}
		o.in = file
		return nil
	}
	if _, found := o.inMore[number]; found {
		return fmt.Errorf("--in gives parameter $%d twice", number)
	}
	if o.inMore == nil {
		o.inMore = map[int]string{}
	}
	o.inMore[number] = file
	return nil
}

// inFiles returns the --in files in parameter order; they must number $1 up
// without a gap.
func (o statementOptions) inFiles() ([]string, error) {
	files := []string{o.in}
	for number := range o.inMore {
		for len(files) < number {
			files = append(files, "")
		}
		files[number-1] = o.inMore[number]
	}
	for i, file := range files {
		if file == "" {
			return nil, fmt.Errorf("--in gives parameters up to $%d but not $%d", len(files), i+1)
		}
	}
	return files, nil
}
