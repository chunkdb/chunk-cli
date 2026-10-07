package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/internal/chunkuri"
)

// fakeTable is a table of the fake server; chunks hold their state bytes
// (payload, then presence) and extra each chunk's extra data by block index.
type fakeTable struct {
	name      string
	blockBits int
	width     int
	height    int
	chunks    map[string][]byte
	versions  map[string]uint64
	blocks    map[string]string
	// extraMaxBlockBits is 0 for a table without extra data.
	extraMaxBlockBits  int
	extraMaxChunkBytes int
	extra              map[string]map[int]extraValue
}

func (t *fakeTable) geometry() geometry {
	return geometry{blockBits: t.blockBits, width: t.width, height: t.height}
}

func (t *fakeTable) info(name string) string {
	return fmt.Sprintf("table=%s\nstore_id=00\nblock_bits=%d\nchunk_width_blocks=%d\nchunk_height_blocks=%d\n"+
		"large_chunk_width_chunks=8\nlarge_chunk_height_chunks=8\ndurability_mode=relaxed\n"+
		"checkpoint_updates=1000\ncheckpoint_wal_bytes=1048576\nwal_group_commit_updates=64\ncheckpoint_compression=none\n"+
		"extra_max_block_bits=%d\nextra_max_chunk_bytes=%d\n",
		name, t.blockBits, t.width, t.height, t.extraMaxBlockBits, t.extraMaxChunkBytes)
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if a%b != 0 && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// locate returns the chunk key and block index of world block (x, y).
func (t *fakeTable) locate(xs, ys string) (string, int) {
	x, _ := strconv.ParseInt(xs, 10, 64)
	y, _ := strconv.ParseInt(ys, 10, 64)
	w, h := int64(t.width), int64(t.height)
	cx, cy := floorDiv(x, w), floorDiv(y, h)
	return fmt.Sprintf("%d:%d", cx, cy), int((y-cy*h)*w + (x - cx*w))
}

func (t *fakeTable) present(key string, index int) bool {
	presence := t.state(key)[t.geometry().payloadBytes():]
	return presence[index/8]>>(index%8)&1 == 1
}

// extraSection encodes a chunk's values in ascending block index.
func (t *fakeTable) extraSection(key string) []byte {
	values := make([]extraValue, 0, len(t.extra[key]))
	for _, v := range t.extra[key] {
		values = append(values, v)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].block < values[j].block })
	return encodeExtraSection(values)
}

func (t *fakeTable) extraDisabled() string {
	return "-ERR INVALID_ARGUMENT extra data is not enabled on table '" + t.name + "' (TABLESET " + t.name + " extra_max_block_bits <bits>)\r\n"
}

func (t *fakeTable) state(key string) []byte {
	if data, ok := t.chunks[key]; ok {
		return data
	}
	g := t.geometry()
	return make([]byte, g.payloadBytes()+g.presenceBytes())
}

type fakeServer struct {
	mu       sync.Mutex
	token    string
	tables   map[string]*fakeTable
	commands []string
	lines    []string
	clock    uint64
	// legacy answers HELLO like a 1.x server.
	legacy bool
	// noExtraData answers HELLO like a server without extra data.
	noExtraData bool
}

func newFakeTable(blockBits, width, height int) *fakeTable {
	return &fakeTable{
		blockBits: blockBits, width: width, height: height,
		chunks: map[string][]byte{}, versions: map[string]uint64{}, blocks: map[string]string{},
		extra: map[string]map[int]extraValue{},
	}
}

// startFakeServer serves protocol 2 on one connection at a time: `default`
// has 2x2 blocks of 4 bits (2 payload bytes, 1 presence byte) and extra data
// of up to 64 bits per block, `sky` 2x2 blocks of 2 bits and no extra data.
func startFakeServer(t *testing.T, server *fakeServer) string {
	t.Helper()
	if server.tables == nil {
		withExtra := newFakeTable(4, 2, 2)
		withExtra.extraMaxBlockBits, withExtra.extraMaxChunkBytes = 64, 1024
		server.tables = map[string]*fakeTable{"default": withExtra, "sky": newFakeTable(2, 2, 2)}
	}
	for name, table := range server.tables {
		table.name = name
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go server.serve(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return "chunk://" + server.token + "@" + ln.Addr().String() + "/"
}

func (s *fakeServer) recorded() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// recordedLines returns the request lines as received.
func (s *fakeServer) recordedLines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

func (s *fakeServer) serve(conn net.Conn) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	writer := bufio.NewWriter(conn)
	greeted := false
	var table *fakeTable
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(strings.TrimRight(line, "\r\n"))
		if len(fields) == 0 {
			continue
		}
		cmd := strings.ToUpper(fields[0])
		var payload []byte
		if cmd == "CHUNKPUT" || cmd == "XPUT" {
			// The real server refuses these payloads unread and closes.
			if refused := refusedPayload(fields, greeted && table != nil); refused != "" {
				_, _ = writer.WriteString(refused)
				_ = writer.Flush()
				return
			}
			length, _ := strconv.Atoi(fields[len(fields)-1])
			payload = make([]byte, length+2)
			if _, err := io.ReadFull(reader, payload); err != nil {
				return
			}
			payload = payload[:length]
		}
		s.mu.Lock()
		s.commands = append(s.commands, cmd)
		s.lines = append(s.lines, strings.Join(fields, " "))
		var reply string
		switch {
		case cmd == "HELLO" && s.legacy:
			reply = "-ERR UNKNOWN_COMMAND HELLO\r\n"
		case cmd == "HELLO":
			reply, table, greeted = s.hello(fields)
		case !greeted:
			reply = "-ERR PROTOCOL expected HELLO 2\r\n"
		default:
			reply, table = s.command(cmd, fields[1:], payload, table)
		}
		s.mu.Unlock()
		if _, err := writer.WriteString(reply); err != nil || writer.Flush() != nil {
			return
		}
		if cmd == "QUIT" || reply == "-ERR PROTOCOL expected HELLO 2\r\n" {
			return
		}
	}
}

// refusedPayload is the reply of the real server's PlanPayload to a payload
// command it refuses unread (and then closes the connection): no table, or a
// coordinate that is not an integer.
func refusedPayload(fields []string, hasTable bool) string {
	if !hasTable {
		return "-ERR NO_TABLE no table selected\r\n"
	}
	for _, field := range fields[1:3] {
		digits := strings.TrimPrefix(field, "-")
		if digits == "" || strings.Trim(digits, "0123456789") != "" {
			return "-ERR INVALID_ARGUMENT invalid integer: " + field + "\r\n"
		}
	}
	return ""
}

func bulk(data string) string { return "$" + strconv.Itoa(len(data)) + "\r\n" + data + "\r\n" }

func (s *fakeServer) hello(fields []string) (string, *fakeTable, bool) {
	token, tableName := "", "default"
	for i := 2; i+1 < len(fields); i += 2 {
		switch strings.ToUpper(fields[i]) {
		case "AUTH":
			token = fields[i+1]
		case "TABLE":
			tableName = fields[i+1]
		}
	}
	if s.token != "" && token == "" {
		return "-ERR AUTH_REQUIRED use HELLO 2 AUTH <token>\r\n", nil, false
	}
	if s.token != "" && token != s.token {
		return "-ERR AUTH_FAILED invalid token\r\n", nil, false
	}
	reply := "protocol=2\nserver_version=test\ncapabilities=zrle,extra-data\nmax_line_bytes=65536\n" +
		"max_area_chunks=256\nmax_response_bytes=67108864\nmax_scan_limit=1024\nmax_batch_ops=1024\n" +
		"max_extra_chunk_bytes=16777216\n"
	if s.noExtraData {
		reply = strings.Replace(strings.Replace(reply, ",extra-data", "", 1), "max_extra_chunk_bytes=16777216\n", "", 1)
	}
	table, ok := s.tables[tableName]
	if !ok && len(fields) > 2 && strings.Contains(strings.ToUpper(strings.Join(fields, " ")), "TABLE") {
		return "-ERR NO_TABLE table '" + tableName + "' does not exist\r\n", nil, false
	}
	if ok {
		reply += table.info(tableName)
	}
	return bulk(reply), table, true
}

func (s *fakeServer) command(cmd string, args []string, payload []byte, table *fakeTable) (string, *fakeTable) {
	switch cmd {
	case "PING":
		return "+PONG\r\n", table
	case "QUIT":
		return "+BYE\r\n", table
	case "TABLES":
		return "*2\r\n" + bulk("default") + bulk("sky"), table
	case "USE", "TABLEINFO":
		next, ok := s.tables[args[0]]
		if !ok {
			return "-ERR NO_TABLE table '" + args[0] + "' does not exist\r\n", table
		}
		if cmd == "USE" {
			table = next
		}
		return bulk(next.info(args[0])), table
	case "TABLESET":
		// The extra data options apply, so TABLEINFO shows them.
		if target, ok := s.tables[args[0]]; ok {
			for i := 1; i+1 < len(args); i += 2 {
				value, _ := strconv.Atoi(args[i+1])
				switch args[i] {
				case "extra_max_block_bits":
					target.extraMaxBlockBits = value
				case "extra_max_chunk_bytes":
					target.extraMaxChunkBytes = value
				}
			}
		}
		return "+OK\r\n", table
	case "TABLECREATE", "TABLEDROP":
		return "+OK\r\n", table
	}
	if table == nil {
		return "-ERR NO_TABLE no table selected\r\n", table
	}
	g := table.geometry()
	switch cmd {
	case "GET":
		bits, ok := table.blocks[args[0]+":"+args[1]]
		if !ok {
			return "$-1\r\n", table
		}
		return bulk(bits), table
	case "SET":
		table.blocks[args[0]+":"+args[1]] = args[2]
		return "+OK\r\n", table
	case "UNSET":
		delete(table.blocks, args[0]+":"+args[1])
		return "+OK\r\n", table
	case "MGET":
		reply := "*" + strconv.Itoa(len(args)/2) + "\r\n"
		for i := 0; i+1 < len(args); i += 2 {
			if bits, ok := table.blocks[args[i]+":"+args[i+1]]; ok {
				reply += bulk(bits)
			} else {
				reply += "$-1\r\n"
			}
		}
		return reply, table
	case "CHUNKEXISTS":
		state := table.state(args[0] + ":" + args[1])
		if bytes.Count(state[g.payloadBytes():], []byte{0}) == g.presenceBytes() {
			return "+0\r\n", table
		}
		return "+1\r\n", table
	case "CHUNKVER":
		return bulk(strconv.FormatUint(table.versions[args[0]+":"+args[1]], 10)), table
	case "XGET", "XPUT", "XDEL":
		if table.extraMaxBlockBits == 0 {
			return table.extraDisabled(), table
		}
		key, index := table.locate(args[0], args[1])
		switch cmd {
		case "XGET":
			value, ok := table.extra[key][index]
			if !ok {
				return "$-1\r\n", table
			}
			return bulk(string(binary.LittleEndian.AppendUint32(nil, uint32(value.bitLength))) + string(value.bytes)), table
		case "XPUT":
			bitLength, _ := strconv.Atoi(args[2])
			if bitLength == 0 || bitLength > table.extraMaxBlockBits {
				return "-ERR INVALID_ARGUMENT XPUT bit_length must be between 1 and extra_max_block_bits (" +
					strconv.Itoa(table.extraMaxBlockBits) + ")\r\n", table
			}
			if len(payload) != extraValueBytes(bitLength) {
				return "-ERR INVALID_ARGUMENT XPUT length does not match its bit_length\r\n", table
			}
			if !table.present(key, index) {
				return "-ERR INVALID_ARGUMENT block (" + args[0] + "," + args[1] + ") is not set; extra data belongs to a present block\r\n", table
			}
			if table.extra[key] == nil {
				table.extra[key] = map[int]extraValue{}
			}
			table.extra[key][index] = extraValue{block: index, bitLength: bitLength, bytes: payload}
		default:
			delete(table.extra[key], index)
		}
		return "+OK\r\n", table
	case "CHUNKGET":
		key := args[0] + ":" + args[1]
		state := table.state(key)
		options := strings.ToUpper(strings.Join(args[2:], " "))
		data := state[:g.payloadBytes()]
		if strings.Contains(options, "STATE") {
			data = state
		}
		if strings.Contains(options, "EXTRA") {
			if table.extraMaxBlockBits == 0 {
				return table.extraDisabled(), table
			}
			data = append(append([]byte(nil), state...), table.extraSection(key)...)
		}
		if strings.Contains(options, "ZRLE") {
			data = encodeZrle(data)
		}
		return bulk(string(data)), table
	case "CHUNKPUT":
		key := args[0] + ":" + args[1]
		options := strings.ToUpper(strings.Join(args[2:len(args)-1], " "))
		data := payload
		size := g.payloadBytes()
		if strings.Contains(options, "STATE") {
			size += g.presenceBytes()
		}
		extra := strings.Contains(options, "EXTRA")
		maxSize := size
		if extra {
			if table.extraMaxBlockBits == 0 {
				return table.extraDisabled(), table
			}
			maxSize += table.extraMaxChunkBytes
		}
		if strings.Contains(options, "ZRLE") {
			decoded, err := decodeZrleRange(payload, size, maxSize)
			if err != nil {
				return "-ERR INVALID_ARGUMENT zrle payload is invalid\r\n", table
			}
			data = decoded
		}
		if len(data) < size || len(data) > maxSize {
			return "-ERR INVALID_ARGUMENT payload length does not match\r\n", table
		}
		var values []extraValue
		if extra {
			var err error
			if values, err = decodeExtraSection(data[size:], g.blockCount()); err != nil {
				return "-ERR INVALID_ARGUMENT " + err.Error() + "\r\n", table
			}
			data = data[:size]
		}
		if index := strings.Index(options, "IF "); index >= 0 {
			want, _ := strconv.ParseUint(strings.Fields(options[index+3:])[0], 10, 64)
			if want != table.versions[key] {
				return "-ERR VERSION_MISMATCH current=" + strconv.FormatUint(table.versions[key], 10) + "\r\n", table
			}
		}
		if !strings.Contains(options, "STATE") {
			data = append(append([]byte(nil), data...), bytes.Repeat([]byte{0xff}, g.presenceBytes())...)
			data[len(data)-1] = byte(1<<(g.blockCount()%8)) - 1
			if g.blockCount()%8 == 0 {
				data[len(data)-1] = 0xff
			}
		}
		s.clock++
		table.chunks[key] = data
		table.versions[key] = s.clock
		// With EXTRA the values are replaced, otherwise those of absent
		// blocks go.
		if extra {
			table.extra[key] = map[int]extraValue{}
			for _, v := range values {
				table.extra[key][v.block] = v
			}
		}
		for index := range table.extra[key] {
			if !table.present(key, index) {
				delete(table.extra[key], index)
			}
		}
		return bulk(strconv.FormatUint(s.clock, 10)), table
	case "CHUNKRANGE":
		reply := []string{}
		for key, state := range table.chunks {
			cx, cy, _ := strings.Cut(key, ":")
			if args[0] <= cx && cx <= args[2] && args[1] <= cy && cy <= args[3] {
				reply = append(reply, bulk(cx+" "+cy)+bulk(string(state)))
			}
		}
		return "*" + strconv.Itoa(2*len(reply)) + "\r\n" + strings.Join(reply, ""), table
	case "CHUNKBATCH":
		return bulk("7"), table
	}
	return "-ERR UNKNOWN_COMMAND " + cmd + "\r\n", table
}

func dialSession(t *testing.T, uri string, table string) (*session, error) {
	t.Helper()
	parsed, err := chunkuri.Parse(uri)
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	client, err := chunkclient.Dial(chunkclient.Config{URI: parsed, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return openSession(client, parsed.Token, table)
}

func runScript(t *testing.T, sess *session, script string) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	if err := runShell(sess, strings.NewReader(script), &out, &errOut); err != nil {
		t.Fatalf("run shell: %v", err)
	}
	return out.String(), errOut.String()
}

func TestShellBlocksAndChunks(t *testing.T) {
	server := &fakeServer{token: "dev-token"}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}

	out, errOut := runScript(t, sess, strings.Join([]string{
		"ping", "get 1 2", "set 1 2 1010", "get 1 2", "mget 1 2 3 4", "unset 1 2", "get 1 2",
		"chunkexists 0 0", "chunkset 0 0 1111000011110000", "chunkexists 0 0", "chunk 0 0", "chunkstate 0 0",
		"chunksetstate 1 0 1010101010101010|1000", "chunkstate 1 0", "chunkget --state 1 0",
		"chunkget --zrle --state 0 0", "chunkrange 0 0 1 0", "quit",
	}, "\n")+"\n")
	if errOut != "" {
		t.Fatalf("unexpected stderr %q", errOut)
	}

	commands := server.recorded()
	want := "HELLO PING GET SET GET MGET UNSET GET CHUNKEXISTS CHUNKPUT CHUNKEXISTS CHUNKGET CHUNKGET " +
		"CHUNKPUT CHUNKGET CHUNKGET CHUNKGET CHUNKRANGE QUIT"
	if strings.Join(commands, " ") != want {
		t.Fatalf("got commands %v, want %s", commands, want)
	}
	for _, expected := range []string{
		"chunk> PONG\n", "chunk> (unset)\n", "chunk> 1010\n", "chunk> 1010\n(unset)\n",
		"chunk> 0\n", "chunk> 1\n", "chunk> 1111000011110000\n", "chunk> 1111000011110000|1111\n",
		"chunk> 1010101010101010|1000\n", "bytes=3\n", "bytes=3 compressed_bytes=",
		"0 0 1111000011110000|1111\n", "1 0 1010101010101010|1000\n", "BYE",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in output %q", expected, out)
		}
	}
	// Bit i of the text is byte i/8, bit i%8: "1111000011110000" is 0x0f 0x0f.
	server.mu.Lock()
	state := server.tables["default"].chunks["0:0"]
	server.mu.Unlock()
	if !bytes.Equal(state, []byte{0x0f, 0x0f, 0x0f}) {
		t.Fatalf("chunk 0 0 stored as % x", state)
	}
}

func TestShellChunkPutVersionsAndSizes(t *testing.T) {
	server := &fakeServer{}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	out, errOut := runScript(t, sess, "chunkput 0 0 a0a1\nchunkput --state --if 1 0 0 a0a10f\n"+
		"chunkput --if 1 0 0 a0a1\nchunkput 0 0 a0\nchunkput --zrle 1 1 0000\nchunkver 0 0\nexit\n")
	if !strings.Contains(out, "chunk> 1\nchunk> 2\n") {
		t.Fatalf("expected versions 1 and 2 in %q", out)
	}
	if !strings.Contains(errOut, "VERSION_MISMATCH current=2") {
		t.Fatalf("expected a version mismatch in %q", errOut)
	}
	if !strings.Contains(errOut, "got 1 bytes, the table's chunks have 2") {
		t.Fatalf("expected a size error in %q", errOut)
	}
	// The size error is caught before sending. Two zero bytes do not shrink,
	// so the --zrle put goes uncompressed.
	commands := strings.Join(server.recorded(), " ")
	if commands != "HELLO CHUNKPUT CHUNKPUT CHUNKPUT CHUNKPUT CHUNKVER" {
		t.Fatalf("got commands %q", commands)
	}
	if !strings.Contains(out, "chunk> 3\nchunk> 2\n") {
		t.Fatalf("expected the zrle put and chunkver in %q", out)
	}
}

func TestShellExitAlias(t *testing.T) {
	uri := startFakeServer(t, &fakeServer{})
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	out, errOut := runScript(t, sess, "exit\n")
	if out != "chunk> " || errOut != "" {
		t.Fatalf("unexpected output %q / %q", out, errOut)
	}
}

func TestShellTables(t *testing.T) {
	server := &fakeServer{token: "dev-token"}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "default")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	out, errOut := runScript(t, sess, "tables\ntablecreate x block_bits 2\nuse missing\nuse sky\n"+
		"chunkset 0 0 10101010\nchunk 0 0\ntabledrop x\ntablecreate x block_bits\nauth x\nexit\n")

	commands := strings.Join(server.recorded(), " ")
	if commands != "HELLO TABLES TABLECREATE USE USE CHUNKPUT CHUNKGET TABLEDROP" {
		t.Fatalf("got commands %q", commands)
	}
	// The prompt names the selected table; a failed use keeps it, and the
	// chunk commands follow the new table's geometry (8 payload bits on sky).
	if !strings.HasPrefix(out, "chunk:default> default\nsky\n") {
		t.Fatalf("unexpected output start %q", out)
	}
	for _, want := range []string{"chunk:default> table=sky\n", "chunk:sky> 1\n", "chunk:sky> 10101010\n", "chunk:sky> OK\n"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output %q", want, out)
		}
	}
	for _, want := range []string{"NO_TABLE", "usage: tablecreate", `unknown shell command "auth"`} {
		if !strings.Contains(errOut, want) {
			t.Fatalf("expected %q in stderr %q", want, errOut)
		}
	}
}

func TestOpenSessionFailures(t *testing.T) {
	uri := startFakeServer(t, &fakeServer{token: "dev-token"})
	if _, err := dialSession(t, strings.Replace(uri, "dev-token", "wrong", 1), ""); err == nil ||
		!strings.Contains(err.Error(), "AUTH_FAILED") {
		t.Fatalf("wrong token: got %v", err)
	}
	if _, err := dialSession(t, strings.Replace(uri, "dev-token@", "", 1), ""); err == nil ||
		!strings.Contains(err.Error(), "AUTH_REQUIRED") {
		t.Fatalf("missing token: got %v", err)
	}
	if _, err := dialSession(t, uri, "missing"); err == nil ||
		!strings.Contains(err.Error(), `connecting with table "missing" failed`) || !strings.Contains(err.Error(), "NO_TABLE") {
		t.Fatalf("missing table: got %v", err)
	}
	legacy := startFakeServer(t, &fakeServer{legacy: true})
	if _, err := dialSession(t, legacy, ""); err == nil || !strings.Contains(err.Error(), "does not speak protocol 2") {
		t.Fatalf("1.x server: got %v", err)
	}
}

func TestSessionWithoutTable(t *testing.T) {
	server := &fakeServer{tables: map[string]*fakeTable{"sky": newFakeTable(2, 2, 2)}}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	// xput refuses locally too: the server would close the connection.
	out, errOut := runScript(t, sess, "chunk 0 0\nxput 0 0 1\nping\nuse sky\nchunk 0 0\nexit\n")
	if strings.Count(errOut, "no table selected") != 2 {
		t.Fatalf("expected two no-table errors, got %q", errOut)
	}
	if !strings.Contains(out, "PONG") || strings.Contains(strings.Join(server.recorded(), " "), "XPUT") {
		t.Fatalf("xput should be refused before sending, out %q", out)
	}
	if !strings.Contains(out, "chunk:sky> 00000000\n") {
		t.Fatalf("expected the chunk after use in %q", out)
	}
}

func TestShellExtraData(t *testing.T) {
	server := &fakeServer{}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	dir := t.TempDir()
	chunkFile := filepath.Join(dir, "chunk.bin")
	valueFile := filepath.Join(dir, "value.bin")
	if err := os.WriteFile(valueFile, []byte{0x0a}, 0o644); err != nil {
		t.Fatalf("write value: %v", err)
	}

	out, errOut := runScript(t, sess, strings.Join([]string{
		"chunkset 0 0 1111000011110000", "xput 1 0 101100000001", "xget 1 0", "xget --bits 1 0",
		"xput --hex --bit-length 3 0 1 07", "xput 5 5 1", "xput --hex --bit-length 12 0 0 0d",
		"chunkget --extra 0 0", "chunkget --extra --zrle 0 0", "chunkget --extra --out " + chunkFile + " 0 0",
		"xdel 1 0", "xget 1 0", "chunkput --extra --zrle --in " + chunkFile + " 0 0", "xget --bits 1 0",
		"chunkset -1 -1 0000000000000000", "xput -1 -1 11", "chunkget --extra -1 -1",
		"chunkbatch 0 0 XPUT 1 1 1 XDEL 0 1", "xput --bit-length 4 --in " + valueFile + " 0 0", "xget 0 0",
		"use sky", "xget 0 0", "xdel 0 0", "exit",
	}, "\n")+"\n")

	commands := strings.Join(server.recorded(), " ")
	want := "HELLO CHUNKPUT XPUT XGET XGET XPUT XPUT CHUNKGET CHUNKGET CHUNKGET XDEL XGET CHUNKPUT XGET " +
		"CHUNKPUT XPUT CHUNKGET CHUNKBATCH XPUT XGET USE XGET XDEL"
	if commands != want {
		t.Fatalf("got commands %q, want %q", commands, want)
	}
	lines := server.recordedLines()
	for _, want := range []string{"XPUT 1 0 12 2", "XPUT 0 1 3 1", "CHUNKGET 0 0 STATE EXTRA", "CHUNKGET 0 0 STATE EXTRA ZRLE",
		"CHUNKGET -1 -1 STATE EXTRA", "CHUNKBATCH 0 0 XPUT 1 1 1 XDEL 0 1", "XPUT 0 0 4 1"} {
		found := false
		for _, line := range lines {
			found = found || line == want
		}
		if !found {
			t.Fatalf("expected request %q in %q", want, lines)
		}
	}
	// 3 state bytes, then block 1 (8 + 2 bytes) and block 2 (8 + 1 bytes).
	for _, expected := range []string{
		"chunk> OK\nchunk> bit_length=12\n0d08\nchunk> 101100000001\nchunk> OK\n",
		"chunk> bytes=22\n00000000  0f 0f 0f 01 00 00 00 0c  00 00 00 0d 08 02 00 00  |................|\n",
		"extra_bytes=19 extra_values=2\nx=1 y=0 block=1 bit_length=12 hex=0d08\nx=0 y=1 block=2 bit_length=3 hex=07\n",
		"chunk> bytes=22 compressed_bytes=", "chunk> wrote 22 bytes to " + chunkFile + "\n",
		"chunk> OK\nchunk> (none)\nchunk> 2\nchunk> 101100000001\n",
		"extra_bytes=9 extra_values=1\nx=-1 y=-1 block=3 bit_length=2 hex=03\n",
		"chunk> 7\nchunk> OK\nchunk> bit_length=4\n0a\n",
	} {
		if !strings.Contains(out, expected) {
			t.Fatalf("expected %q in output %q", expected, out)
		}
	}
	for _, expected := range []string{
		"error: xput failed: INVALID_ARGUMENT block (5,5) is not set",
		"error: a value of 12 bits takes 2 bytes, got 1\n",
		"error: xget failed: INVALID_ARGUMENT extra data is not enabled on table 'sky'",
		"error: xdel failed: INVALID_ARGUMENT extra data is not enabled on table 'sky'",
	} {
		if !strings.Contains(errOut, expected) {
			t.Fatalf("expected %q in stderr %q", expected, errOut)
		}
	}
	if strings.Count(errOut, "error: ") != 4 {
		t.Fatalf("expected 4 errors in %q", errOut)
	}
}

func TestShellTableExtraOptions(t *testing.T) {
	server := &fakeServer{}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	out, errOut := runScript(t, sess, "tablecreate world block_bits 4 extra_max_block_bits 4096 extra_max_chunk_bytes 65536\n"+
		"tableinfo sky\ntableset sky extra_max_block_bits 32 extra_max_chunk_bytes 4096\ntableinfo sky\nuse sky\nxget 0 0\nexit\n")
	if errOut != "" {
		t.Fatalf("unexpected stderr %q", errOut)
	}
	lines := server.recordedLines()
	if lines[1] != "TABLECREATE world block_bits 4 extra_max_block_bits 4096 extra_max_chunk_bytes 65536" ||
		lines[3] != "TABLESET sky extra_max_block_bits 32 extra_max_chunk_bytes 4096" {
		t.Fatalf("got requests %q", lines)
	}
	// tableinfo shows the options, 0 before extra data is enabled; the
	// enabled table then answers xget.
	for _, want := range []string{
		"extra_max_block_bits=0\nextra_max_chunk_bytes=0\n",
		"extra_max_block_bits=32\nextra_max_chunk_bytes=4096\n",
		"chunk:sky> (none)\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected %q in output %q", want, out)
		}
	}
}

// Coordinates take the server's integer form: a leading '+' is refused
// before sending (in a payload header the server would close the
// connection), and negative coordinates need no "--".
func TestShellCoordinateForms(t *testing.T) {
	server := &fakeServer{}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	out, errOut := runScript(t, sess, strings.Join([]string{
		"chunkput +0 0 a0a1", "chunkput 0 +0 a0a1", "ping",
		"chunkput -1 -1 a0a1", "chunkput --state --if 1 -1 -1 a0a10f", "chunkget -1 -1",
		"chunkbatch -1 -1 SET -2 -2 0011", "exit",
	}, "\n")+"\n")
	if strings.Count(errOut, "not an integer") != 2 {
		t.Fatalf("expected two refused coordinates, got %q", errOut)
	}
	commands := strings.Join(server.recorded(), " ")
	if want := "HELLO PING CHUNKPUT CHUNKPUT CHUNKGET CHUNKBATCH"; commands != want {
		t.Fatalf("got commands %q, want %q (errors %q)", commands, want, errOut)
	}
	if !strings.Contains(out, "PONG") {
		t.Fatalf("connection should survive the refused commands: %q", out)
	}
}

func TestShellWithoutExtraData(t *testing.T) {
	server := &fakeServer{noExtraData: true}
	uri := startFakeServer(t, server)
	sess, err := dialSession(t, uri, "")
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	_, errOut := runScript(t, sess, "xput 0 0 1\nchunkget --extra 0 0\nchunkput --extra 0 0 000000\nexit\n")
	if strings.Count(errOut, "the server does not support extra data") != 3 {
		t.Fatalf("expected three refusals in %q", errOut)
	}
	if commands := strings.Join(server.recorded(), " "); commands != "HELLO" {
		t.Fatalf("got commands %q", commands)
	}
}
