package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func TestStatementReplyWire(t *testing.T) {
	const null = "_\r\n"
	const number = ":9007199254740992\r\n"
	const epoch = "0123456789abcdef0123456789abcdef"
	progress := watchWireMap("files_done", ":1\r\n", "files_total", ":2\r\n", "bytes_done", ":3\r\n", "bytes_total", ":4\r\n")
	connection := func(progress string) string {
		return "*1\r\n" + watchWireMap("id", number, "user", null, "peer", watchWireBulk("peer"),
			"state", watchWireBulk("running"), "statement", watchWireBulk("RESTORE TABLE"), "progress", progress)
	}
	cut := watchWireMap("table", watchWireBulk("world"), "epoch", watchWireBulk(epoch), "revision", number)
	backup := func(tables string) string {
		return watchWireMap("tables", tables, "files", ":8\r\n", "bytes", ":632\r\n", "cuts", "*1\r\n"+cut)
	}
	restore := func(epochField string) string {
		return watchWireMap("table", watchWireBulk("new_world"), "epoch", watchWireBulk(epoch), "parent_epoch", epochField,
			"fork_revision", number, "files", ":3\r\n", "bytes", ":245\r\n")
	}
	users := func(grants string) string {
		return "*1\r\n" + watchWireMap("name", watchWireBulk("bot"), "manages_users", "#f\r\n",
			"requires_channel_binding", "#t\r\n", "verifiers", ":2\r\n", "grants", grants,
			"server_rights", "*1\r\n"+watchWireBulk("MONITOR"))
	}
	migration := func(user string) string {
		return "*1\r\n" + watchWireMap("name", watchWireBulk("a"), "applied_ms", ":123\r\n", "user", user, "statement", watchWireBulk("DROP TABLE w"))
	}
	scan := func(row, cursor string) string {
		return watchWireMap("chunks", "*1\r\n"+row, "more", "#t\r\n", "after", cursor,
			"epoch", watchWireBulk(epoch), "position", null, "retained_deletion_floor", ":1\r\n")
	}
	const cursor = "*3\r\n:0\r\n:0\r\n:1\r\n"
	const scanRow = "*4\r\n:0\r\n:0\r\n:0\r\n" + number
	for _, tc := range []struct {
		statement, reply, text, json, broken, errorPart string
	}{
		{"PING", "+PONG\r\n", "PONG\n", "\"PONG\"\n", "+OK\r\n", "expected PONG"},
		{"FLUSH WAL", "+OK\r\n", "OK\n", "\"OK\"\n", null, "expected OK"},
		{"SET BLOCK 0 0 IN world n=1", number, "9007199254740992\n", "\"9007199254740992\"\n", null, "invalid revision"},
		{"COMPACT TOMBSTONES world BEFORE 2", ":2\r\n", "2\n", "2\n", ":-1\r\n", "invalid revision"},
		{"SHOW TABLES", "*1\r\n" + watchWireBulk("world"), "1) world\n", "[\"world\"]\n", "*1\r\n" + null, "invalid table name"},
		{"SHOW METRICS", watchWireBulk("chunkdb_metric 1\n"), "chunkdb_metric 1\n", "\"chunkdb_metric 1\\n\"\n", "+OK\r\n", "expected UTF-8 text"},
		{"CANCEL 0", watchWireMap("cancelled", "#f\r\n"), "cancelled = false\n", "{\"cancelled\":false}\n", watchWireMap("cancelled", ":0\r\n"), "invalid or missing cancelled"},
		{"KILL CONNECTION 0", watchWireMap("killed", "#f\r\n"), "killed = false\n", "{\"killed\":false}\n", watchWireMap("killed", "#f\r\n", "killed", "#t\r\n"), "duplicate killed"},
		{"MIGRATE 'a' DROP TABLE w", "+skipped\r\n", "skipped\n", "\"skipped\"\n", "+OK\r\n", "expected applied or skipped"},
		{"SHOW MIGRATIONS", migration(null), "1) name = a, applied_ms = 123, user = NULL, statement = DROP TABLE w\n",
			`[{"name":"a","applied_ms":123,"user":null,"statement":"DROP TABLE w"}]` + "\n", migration(":0\r\n"), "invalid or missing user"},
		{"SHOW CONNECTIONS", connection(progress), "1)\n  id = 9007199254740992\n  user = NULL\n  peer = peer\n  state = running\n  statement = RESTORE TABLE\n  progress:\n    files_done = 1\n    files_total = 2\n    bytes_done = 3\n    bytes_total = 4\n",
			`[{"id":"9007199254740992","user":null,"peer":"peer","state":"running","statement":"RESTORE TABLE","progress":{"files_done":1,"files_total":2,"bytes_done":3,"bytes_total":4}}]` + "\n",
			connection(watchWireMap("files_done", ":1\r\n")), "invalid or missing files_total"},
		{"SHOW USERS", users(watchWireMap("world", watchWireBulk("READ"))), "1)\n  name = bot\n  manages_users = false\n  requires_channel_binding = true\n  verifiers = 2\n  grants:\n    world = READ\n  server_rights = [MONITOR]\n",
			`[{"name":"bot","manages_users":false,"requires_channel_binding":true,"verifiers":2,"grants":{"world":"READ"},"server_rights":["MONITOR"]}]` + "\n",
			users(watchWireMap("world", ":1\r\n")), "invalid grant"},
		{"BACKUP TO 'snapshot'", backup(":1\r\n"), "tables = 1\nfiles = 8\nbytes = 632\ncuts:\n  1) table = world, epoch = " + epoch + ", revision = 9007199254740992\n",
			`{"tables":1,"files":8,"bytes":632,"cuts":[{"table":"world","epoch":"` + epoch + `","revision":"9007199254740992"}]}` + "\n",
			backup(":2\r\n"), "table count and cuts disagree"},
		{"RESTORE TABLE world FROM 'snapshot' AS new_world", restore(watchWireBulk(epoch)),
			"table = new_world\nepoch = " + epoch + "\nparent_epoch = " + epoch + "\nfork_revision = 9007199254740992\nfiles = 3\nbytes = 245\n",
			`{"table":"new_world","epoch":"` + epoch + `","parent_epoch":"` + epoch + `","fork_revision":"9007199254740992","files":3,"bytes":245}` + "\n",
			restore(watchWireBulk("bad")), "invalid or missing parent_epoch"},
		{"AGGREGATE CHUNK 0 0 FROM world COUNT(*), AVG(n)", watchWireMap("count(*)", ":1\r\n", "avg(n)", ",2.5\r\n"), "count(*) = 1\navg(n) = 2.5\n",
			`{"count(*)":1,"avg(n)":2.5}` + "\n", watchWireMap("count(*)", null, "avg(n)", ":2\r\n"), "invalid result for count(*)"},
		{"AGGREGATE CHUNK 0 0 0 FROM world MAX(n) WHERE tag = 'BY CHUNK' BY CHUNK", "*1\r\n" + scanRow, "1) [0, 0, 0, 9007199254740992]\n",
			`[[0,0,0,"9007199254740992"]]` + "\n", "*1\r\n*3\r\n:0\r\n:0\r\n:0\r\n", "row does not match"},
		{"SCAN CHUNKS FROM world WHERE n > 0 LIMIT 1", scan(scanRow, cursor),
			"chunks:\n  1) [0, 0, 0, 9007199254740992]\nmore = true\nafter = [0, 0, 1]\nepoch = " + epoch + "\nposition = NULL\nretained_deletion_floor = 1\n",
			`{"chunks":[[0,0,0,"9007199254740992"]],"more":true,"after":[0,0,1],"epoch":"` + epoch + `","position":null,"retained_deletion_floor":1}` + "\n",
			scan(scanRow, "*2\r\n:0\r\n:0\r\n"), "invalid after coordinate"},
	} {
		t.Run(tc.statement, func(t *testing.T) {
			for _, form := range []struct {
				json                bool
				wire, want, failure string
			}{
				{false, tc.reply, tc.text, ""},
				{true, tc.reply, tc.json, ""},
				{false, tc.broken, "", tc.errorPart},
				{true, tc.broken, "", tc.errorPart},
			} {
				uri, done := fakeMigrationServer(t, []string{tc.statement}, []string{form.wire})
				args := []string{"--uri", uri}
				if form.json {
					args = append(args, "--json")
				}
				var out, failure bytes.Buffer
				code := run(append(args, tc.statement), strings.NewReader(""), &out, &failure)
				if form.failure == "" {
					if code != 0 || out.String() != form.want || failure.Len() != 0 {
						t.Fatalf("got %d, stdout %q, stderr %q; want %q", code, out.String(), failure.String(), form.want)
					}
				} else if code != 1 || out.Len() != 0 || !strings.Contains(failure.String(), form.failure) {
					t.Fatalf("bad reply got %d, stdout %q, stderr %q; want %q", code, out.String(), failure.String(), form.failure)
				}
				waitSlotServer(t, done)
			}
		})
	}
}

func TestStatementReplyClassification(t *testing.T) {
	scan := classify(`SCAN CHUNKS FROM world WHERE n > limit AND tag = 'WITH DATA' COLUMNS (kind, columns) WITH DATA SINCE 3LIMIT 10 AFTER 0 1 0`)
	if scan.kind != statementScanChunks || !scan.scanWhere || scan.scanSince == nil || *scan.scanSince != 3 ||
		scan.scanAfter == nil || *scan.scanAfter != [3]int64{0, 1, 0} || strings.Join(scan.columns, ",") != "kind,columns" {
		t.Fatalf("data scan: %+v", scan)
	}
	if scan := classify(`SCAN CHUNKS FROM world WHERE tag = 'WITH DATA' AND columns`); scan.kind != statementScanIndex || !scan.scanWhere {
		t.Fatalf("metadata scan: %+v", scan)
	}
	for _, statement := range []string{
		"SET BLOCK 0 0IN world returning=42RETURNING returning, kind",
		"DELETE BLOCK 0 0FROM world RETURNING OLD returning, kind",
	} {
		got := classify(statement)
		if got.kind != statementReturning || got.table != "world" || strings.Join(got.columns, ",") != "returning,kind" {
			t.Errorf("%s: %+v", statement, got)
		}
	}
	if got := classify(`SET BLOCK 0 0 IN world returning = kind`); got.kind != statementOther {
		t.Fatalf("plain write: %+v", got)
	}
	if got := classify("GET BLOCK 0 0FROM world"); got.kind != statementGetBlock || got.table != "world" {
		t.Fatalf("compact block: %+v", got)
	}
}

func TestShellRejectsMalformedTransactionReply(t *testing.T) {
	for _, control := range []struct{ statement, reply, failure string }{
		{"BEGIN", "+PONG\r\n", "BEGIN: expected OK"},
		{"COMMIT", "#t\r\n", "COMMIT: invalid revision"},
		{"ROLLBACK", ":1\r\n", "ROLLBACK: expected OK"},
	} {
		statements, replies := []string{control.statement}, []string{control.reply}
		input, want := control.statement+"\n", "chunk> "
		if control.statement != "BEGIN" {
			statements = append([]string{"BEGIN"}, statements...)
			replies = append([]string{"+OK\r\n"}, replies...)
			input, want = "BEGIN\n"+input, "chunk> OK\nchunk*> "
		}
		uri, done := fakeMigrationServer(t, statements, replies)
		var out, failure bytes.Buffer
		code := run([]string{"--uri", uri, "shell"}, strings.NewReader(input), &out, &failure)
		if code != 1 || out.String() != want || !strings.Contains(failure.String(), control.failure) {
			t.Fatalf("%s: exit %d, output %q, stderr %q", control.statement, code, out.String(), failure.String())
		}
		waitSlotServer(t, done)
	}
}

func TestColumnDefaultAcceptsRoundTripFloat32(t *testing.T) {
	column := chunkclient.Column{
		Name:    "weight",
		Type:    chunkclient.ColumnType{Kind: chunkclient.ColumnFloat32},
		Default: chunkclient.Value{Kind: chunkclient.KindDouble, Text: "3.4028235e+38"},
	}
	if err := validateColumnDefaults("DESCRIBE", []chunkclient.Column{column}); err != nil {
		t.Fatal(err)
	}
	column.Default.Text = "1e39"
	if err := validateColumnDefaults("DESCRIBE", []chunkclient.Column{column}); err == nil {
		t.Fatal("accepted finite f32 overflow")
	}
}
