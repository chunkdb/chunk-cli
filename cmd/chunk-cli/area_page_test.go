package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
)

func areaPageFixture(t *testing.T) (*chunkclient.Schema, statementInfo, chunkclient.Value) {
	t.Helper()
	chunk := outputModelChunk(t)
	schema := &chunkclient.Schema{Version: 7, Dims: 3, ChunkWidth: 2, ChunkHeight: 1, ChunkDepth: 1,
		Columns: chunk.Columns, ChunkColumns: chunk.MetadataColumns}
	info := classify("GET AREA -1 2 0 TO 2 2 1 FROM world LIMIT 2")
	if info.areaPage == nil {
		t.Fatal(info)
	}
	info.areaPage.maxChunks = 4096
	page := foundationMapValue(watchBulk("chunks"), watchArray(watchArray(watchInt("-1"), watchInt("2"), watchInt("0"), chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: outputModelForm(t)})),
		watchBulk("more"), chunkclient.Value{Kind: chunkclient.KindBool, Bool: true}, watchBulk("after"), watchArray(watchInt("-1"), watchInt("2"), watchInt("1")))
	return schema, info, page
}

func TestAreaPageClassificationAndOutput(t *testing.T) {
	schema, info, page := areaPageFixture(t)
	for _, asJSON := range []bool{false, true} {
		var out bytes.Buffer
		if err := printAreaPage(&out, schema, info, page, statementOptions{json: asJSON, blocks: true}); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			if !strings.Contains(out.String(), `"more":true,"after":[-1,2,1]`) || !strings.Contains(out.String(), `"chunk_columns":{"flag":true,"label":null}`) {
				t.Fatal(out.String())
			}
		} else if !strings.HasPrefix(out.String(), "more = true\nafter = [-1, 2, 1]\nchunk -1 2 0\n") || !strings.Contains(out.String(), "chunk_columns:\n    flag = true\n") {
			t.Fatal(out.String())
		}
		info.withSchema = true
		out.Reset()
		if err := printReadMetadata(&out, schema, info, watchArray(watchInt("7"), page), statementOptions{json: asJSON}); err != nil {
			t.Fatal(err)
		}
		if asJSON {
			var value struct {
				SchemaVersion uint64 `json:"schema_version"`
				Page          struct {
					Chunks []json.RawMessage `json:"chunks"`
					More   bool              `json:"more"`
					After  []int64           `json:"after"`
				} `json:"page"`
			}
			if err := json.Unmarshal(out.Bytes(), &value); err != nil || value.SchemaVersion != 7 || !value.Page.More || len(value.Page.Chunks) != 1 || fmt.Sprint(value.Page.After) != "[-1 2 1]" {
				t.Fatal(out.String(), err)
			}
		} else if !strings.HasPrefix(out.String(), "schema_version = 7\nmore = true\n") {
			t.Fatal(out.String())
		}
	}
	classified := classify(`GET AREA AROUND 1 2 0 RADIUS 3 FROM after COLUMNS limit,after WITH SCHEMA AFTER 0 2 0 LIMIT 2`)
	if classified.areaPage == nil || classified.areaPage.dims != 3 || classified.areaPage.limit != 2 || classified.areaPage.after == nil || *classified.areaPage.after != [3]int64{0, 2, 0} || !classified.withSchema || classified.table != "after" || strings.Join(classified.columns, ",") != "limit,after" {
		t.Fatal(classified)
	}
	for _, statement := range []string{"GET AREA 0 0 TO 1 1 FROM t LIMIT 0", "GET AREA 0 0 TO 1 1 FROM t AFTER 0 0 0", "GET AREA 0 0 TO 1 1 FROM t LIMIT 1 LIMIT 2", "GET AREA 0 0 0 TO 1 1 1 FROM t AFTER 0 0", "GET AREA 0 0 TO 1 1 FROM t AFTER 0 0 AFTER 0 1"} {
		if classify(statement).areaPage != nil {
			t.Fatal(statement)
		}
	}
}

func TestAreaPageRejectsMalformedWithoutOutput(t *testing.T) {
	for _, defect := range []string{"missing", "duplicate", "cursor-arity", "cursor-null", "cursor-overflow", "cursor-outside", "cursor-no-progress", "cursor-skip", "completed-cursor", "completed-early", "order", "row-outside", "row-after-cursor", "limit", "table-arity", "entry", "ball-z"} {
		t.Run(defect, func(t *testing.T) {
			schema, info, page := areaPageFixture(t)
			switch defect {
			case "missing":
				page.Map = page.Map[:2]
			case "duplicate":
				page.Map = append(page.Map, page.Map[2])
			case "cursor-arity":
				page.Map[2].Value.Items = page.Map[2].Value.Items[:2]
			case "cursor-null":
				page.Map[2].Value = chunkclient.Value{Kind: chunkclient.KindNull}
			case "cursor-overflow":
				page.Map[2].Value.Items[0] = watchInt("9223372036854775808")
			case "cursor-outside":
				page.Map[2].Value.Items[0] = watchInt("3")
			case "cursor-no-progress":
				info.areaPage.after = &[3]int64{-1, 2, 1}
			case "cursor-skip":
				page.Map[2].Value.Items[0] = watchInt("0")
			case "completed-cursor":
				page.Map[1].Value.Bool = false
			case "completed-early":
				page.Map[1].Value.Bool = false
				page.Map[2].Value = chunkclient.Value{Kind: chunkclient.KindNull}
			case "order":
				page.Map[0].Value.Items = append(page.Map[0].Value.Items, page.Map[0].Value.Items[0])
			case "row-outside":
				page.Map[0].Value.Items[0].Items[0] = watchInt("-2")
			case "row-after-cursor":
				page.Map[0].Value.Items[0].Items[0] = watchInt("0")
			case "limit":
				info.areaPage.limit = 4097
			case "table-arity":
				schema.Dims = 2
			case "entry":
				page.Map[0].Value.Items[0].Items[3] = chunkclient.Value{Kind: chunkclient.KindNull}
			case "ball-z":
				info.areaPage = parseAreaSelector([]string{"AROUND", "0", "0", "0", "RADIUS", "1"})
				info.areaPage.maxChunks = 4096
				page.Map[0].Value.Items = nil
				page.Map[2].Value = watchArray(watchInt("1"), watchInt("0"), watchInt("1"))
			}
			var out bytes.Buffer
			if err := printAreaPage(&out, schema, info, page, statementOptions{blocks: true}); err == nil || out.Len() != 0 {
				t.Fatal(out.String(), err)
			}
		})
	}
}

func TestAreaPageEmptyAndExactCursor(t *testing.T) {
	schema, info, page := areaPageFixture(t)
	page.Map[0].Value.Items = nil
	info.areaPage = parseAreaSelector([]string{strconv.FormatInt(math.MinInt64, 10), "0", "0", "TO", strconv.FormatInt(math.MaxInt64, 10), "0", "0"})
	info.areaPage.maxChunks = 4096
	page.Map[2].Value = watchArray(watchInt(strconv.FormatInt(math.MinInt64, 10)), watchInt("0"), watchInt("0"))
	var out bytes.Buffer
	if err := printAreaPage(&out, schema, info, page, statementOptions{json: true}); err != nil || !strings.Contains(out.String(), `"after":["-9223372036854775808",0,0]`) {
		t.Fatal(out.String(), err)
	}
	page.Map[1].Value.Bool = false
	page.Map[2].Value = chunkclient.Value{Kind: chunkclient.KindNull}
	info.areaPage.after = &[3]int64{math.MaxInt64, 0, 0}
	out.Reset()
	if err := printAreaPage(&out, schema, info, page, statementOptions{}); err != nil || out.String() != "more = false\nafter = NULL\n(empty)\n" {
		t.Fatal(out.String(), err)
	}
	request := parseAreaSelector([]string{"AROUND", "0", "0", "0", "RADIUS", strconv.FormatInt(math.MaxInt64, 10)})
	if !request.contains([3]int64{math.MaxInt64, 0, 0}) || request.contains([3]int64{math.MaxInt64, 0, 1}) {
		t.Fatal("wide Euclidean boundary")
	}
}

func sparseAreaForm(blocks int) []byte {
	presence := make([]byte, (blocks+7)/8)
	presence[0] = 1
	data := make([]byte, 60)
	copy(data, "CKCF")
	data[4], data[6] = 1, 1
	binary.LittleEndian.PutUint64(data[8:], 9)
	binary.LittleEndian.PutUint64(data[16:], 7)
	binary.LittleEndian.PutUint64(data[24:], 11)
	binary.LittleEndian.PutUint16(data[32:], 2)
	binary.LittleEndian.PutUint16(data[36:], 1)
	binary.LittleEndian.PutUint32(data[40:], 60)
	binary.LittleEndian.PutUint32(data[44:], uint32(len(presence)))
	binary.LittleEndian.PutUint16(data[48:], 6)
	binary.LittleEndian.PutUint32(data[52:], uint32(60+len(presence)))
	binary.LittleEndian.PutUint32(data[56:], 4)
	data = append(data, presence...)
	return append(data, 0, 0, 0, 0)
}

func TestAreaAggregateDecodeBudget(t *testing.T) {
	columns := make([]chunkclient.Value, 1024)
	names := make([]string, len(columns))
	for i := range columns {
		names[i] = fmt.Sprintf("v%d", i)
		columns[i] = foundationMapValue(watchBulk("id"), watchInt(strconv.Itoa(i+1)), watchBulk("name"), watchBulk(names[i]), watchBulk("type"), watchBulk("text(1)"), watchBulk("null"), chunkclient.Value{Kind: chunkclient.KindBool, Bool: true}, watchBulk("required"), chunkclient.Value{Kind: chunkclient.KindBool}, watchBulk("default"), chunkclient.Value{Kind: chunkclient.KindNull})
	}
	schema, err := chunkclient.ParseDescribe(foundationMapValue(watchBulk("table"), watchBulk("t"), watchBulk("version"), watchInt("7"), watchBulk("chunk"), watchArray(watchInt("1024"), watchInt("1")), watchBulk("large"), watchArray(watchInt("1"), watchInt("1")), watchBulk("options"), foundationMapValue(), watchBulk("columns"), watchArray(columns...)))
	if err != nil {
		t.Fatal(err)
	}
	form := sparseAreaForm(1024)
	cost, err := chunkclient.ChunkDecodeArrayBytes(schema, names, form, true)
	if err != nil {
		t.Fatal(err)
	}
	count := int((64*1024*1024)/cost) + 1
	if count > 4096 || cost >= 64*1024*1024 {
		t.Fatal(cost, count)
	}
	rows := make([]chunkclient.Value, count)
	for i := range rows {
		rows[i] = watchArray(watchInt(strconv.Itoa(i)), watchInt("0"), chunkclient.Value{Kind: chunkclient.KindBulk, Bulk: form})
	}
	var out bytes.Buffer
	if err := printArea(&out, schema, names, watchArray(rows...), statementOptions{blocks: true}); err == nil || !strings.Contains(err.Error(), "decoded arrays") || out.Len() != 0 {
		t.Fatal(out.Len(), err)
	}
	if err := printArea(&out, schema, names[:1], watchArray(rows...), statementOptions{blocks: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "v0 = NULL") {
		t.Fatal("narrow projection was not decoded")
	}
}

func TestAreaPageRequiresCapabilityBeforeRequest(t *testing.T) {
	uri, done := rawAdmissionServer(t, func(conn net.Conn, reader *bufio.Reader) error {
		if err := expectSlotLine(reader, "PING"); err != nil {
			return err
		}
		_, err := io.WriteString(conn, "+PONG\r\n")
		return err
	})
	client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var out bytes.Buffer
	err = execute(client, nil, "GET AREA 0 0 TO 1 0 FROM world LIMIT 1", statementOptions{}, &out, console{})
	if err == nil || !strings.Contains(err.Error(), "does not support area_paging") || out.Len() != 0 {
		t.Fatal(out.String(), err)
	}
	if _, err := client.Do("PING"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAreaReplyBudgetRefusesPartialBodyAndCloses(t *testing.T) {
	for _, paged := range []bool{false, true} {
		t.Run(strconv.FormatBool(paged), func(t *testing.T) {
			statement := "GET AREA 0 0 0 TO 1 0 0 FROM world"
			prefix := "*1\r\n*4\r\n:0\r\n:0\r\n:0\r\n$33554432\r\n"
			if paged {
				statement += " LIMIT 1"
				prefix = "%3\r\n$6\r\nchunks\r\n" + prefix
			}
			hello := watchWireMap("protocol", ":3\r\n", "server_version", watchWireBulk("test"), "max_line_bytes", ":1048576\r\n", "max_parameters", ":1024\r\n", "max_area_chunks", ":2\r\n", "max_response_bytes", ":80\r\n", "max_scan_limit", ":4096\r\n", "server_signature", "_\r\n", "features", "*1\r\n"+watchWireBulk("area_paging"), "limits", watchWireMap("max_area_chunks", ":2\r\n", "max_area_response_bytes", ":80\r\n"))
			uri, done := rawAdmissionServer(t, func(conn net.Conn, reader *bufio.Reader) error {
				if err := expectSlotLine(reader, "DESCRIBE world"); err != nil {
					return err
				}
				if err := expectSlotLine(reader, statement); err != nil {
					return err
				}
				if _, err := io.WriteString(conn, modelDescribeWire()+prefix); err != nil {
					return err
				}
				_, err := reader.ReadByte()
				if !errors.Is(err, io.EOF) {
					return fmt.Errorf("expected a closed refused connection, got %v", err)
				}
				return nil
			}, hello)
			client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var out bytes.Buffer
			err = execute(client, nil, statement, statementOptions{}, &out, console{})
			if err == nil || !strings.Contains(err.Error(), "negotiated") || out.Len() != 0 || client.Broken() == nil {
				t.Fatal(out.String(), err)
			}
			if _, err := client.Do("PING"); err == nil || !strings.Contains(err.Error(), "unusable") {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAreaSemanticFailureClosesConnection(t *testing.T) {
	for _, inTxn := range []bool{false, true} {
		for _, defect := range []string{"cursor", "envelope", "form"} {
			t.Run(fmt.Sprintf("transaction=%t/%s", inTxn, defect), func(t *testing.T) {
				statement := "GET AREA 0 0 0 TO 1 0 0 FROM world LIMIT 2 WITH SCHEMA"
				page := watchWireMap("chunks", "*0\r\n", "more", "#t\r\n", "after", "_\r\n")
				if defect == "form" {
					page = watchWireMap("chunks", "*1\r\n*4\r\n:0\r\n:0\r\n:0\r\n"+watchWireBulk("invalid form"), "more", "#f\r\n", "after", "_\r\n")
				}
				reply := "*2\r\n:7\r\n" + page
				if defect == "envelope" {
					reply = "*1\r\n:7\r\n"
				}
				hello := strings.Replace(admissionHello(), "%8\r\n", "%9\r\n", 1) + watchWireBulk("features") + "*1\r\n" + watchWireBulk("area_paging")
				uri, done := rawAdmissionServer(t, func(conn net.Conn, reader *bufio.Reader) error {
					if inTxn {
						if err := expectSlotLine(reader, "BEGIN"); err != nil {
							return err
						}
						if _, err := io.WriteString(conn, "+OK\r\n"); err != nil {
							return err
						}
						if err := expectSlotLine(reader, "SET BLOCK 0 0 0 IN world n=1"); err != nil {
							return err
						}
						if _, err := io.WriteString(conn, "_\r\n"); err != nil {
							return err
						}
					}
					if err := expectSlotLine(reader, "DESCRIBE world"); err != nil {
						return err
					}
					if err := expectSlotLine(reader, statement); err != nil {
						return err
					}
					if _, err := io.WriteString(conn, modelDescribeWire()+reply); err != nil {
						return err
					}
					if _, err := reader.ReadByte(); !errors.Is(err, io.EOF) {
						return fmt.Errorf("malformed socket was reused: %v", err)
					}
					return nil
				}, hello)
				client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				var txn *shellTxn
				if inTxn {
					txn = &shellTxn{}
				}
				var out bytes.Buffer
				if inTxn {
					if err := execute(client, txn, "BEGIN", statementOptions{}, &out, console{}); err != nil {
						t.Fatal(err)
					}
					if err := execute(client, txn, "SET BLOCK 0 0 0 IN world n=1", statementOptions{}, &out, console{}); err != nil {
						t.Fatal(err)
					}
				}
				out.Reset()
				err = execute(client, txn, statement, statementOptions{}, &out, console{})
				var malformed *areaReplyError
				if !errors.As(err, &malformed) || client.Broken() == nil || out.Len() != 0 || inTxn && txn.open {
					t.Fatal(out.String(), err, client.Broken(), txn)
				}
				if inTxn {
					if err := execute(client, txn, "COMMIT", statementOptions{}, &out, console{}); err == nil {
						t.Fatal("malformed read allowed COMMIT")
					}
				} else if _, err := client.Do("PING"); err == nil {
					t.Fatal("malformed connection reused")
				}
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

type areaFailedOutput struct{}

func (areaFailedOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestAreaLocalRefusalsPreserveConnection(t *testing.T) {
	for _, kind := range []string{"decoded-arrays", "schema", "output"} {
		t.Run(kind, func(t *testing.T) {
			statement := "GET AREA 0 0 0 TO 0 0 0 FROM world LIMIT 1 WITH SCHEMA"
			describe := modelDescribeWire()
			reply := "*2\r\n:7\r\n" + watchWireMap("chunks", "*0\r\n", "more", "#f\r\n", "after", "_\r\n")
			if kind == "schema" {
				reply = strings.Replace(reply, ":7\r\n", ":8\r\n", 1)
			}
			if kind == "decoded-arrays" {
				statement = "GET AREA 0 0 TO 12 0 FROM world COLUMNS v"
				column := watchWireMap("id", ":1\r\n", "name", watchWireBulk("v"), "type", watchWireBulk("text(1)"), "null", "#t\r\n", "required", "#f\r\n", "default", "_\r\n")
				describe = watchWireMap("table", watchWireBulk("world"), "version", ":7\r\n", "chunk", "*2\r\n:4096\r\n:256\r\n", "large", "*2\r\n:1\r\n:1\r\n", "options", watchWireMap(), "columns", "*1\r\n"+column)
				form := sparseAreaForm(1 << 20)
				var rows strings.Builder
				rows.WriteString("*13\r\n")
				for i := 0; i < 13; i++ {
					fmt.Fprintf(&rows, "*3\r\n:%d\r\n:0\r\n%s", i, watchWireBulk(string(form)))
				}
				reply = rows.String()
			}
			hello := strings.Replace(admissionHello(), "%8\r\n", "%9\r\n", 1) + watchWireBulk("features") + "*1\r\n" + watchWireBulk("area_paging")
			uri, done := rawAdmissionServer(t, func(conn net.Conn, reader *bufio.Reader) error {
				if err := expectSlotLine(reader, "DESCRIBE world"); err != nil {
					return err
				}
				if err := expectSlotLine(reader, statement); err != nil {
					return err
				}
				if _, err := io.WriteString(conn, describe+reply); err != nil {
					return err
				}
				if err := expectSlotLine(reader, "PING"); err != nil {
					return err
				}
				_, err := io.WriteString(conn, "+PONG\r\n")
				return err
			}, hello)
			client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var out bytes.Buffer
			var writer io.Writer = &out
			if kind == "output" {
				writer = areaFailedOutput{}
			}
			err = execute(client, nil, statement, statementOptions{blocks: kind == "decoded-arrays"}, writer, console{})
			if err == nil || client.Broken() != nil || out.Len() != 0 {
				t.Fatal(err, client.Broken(), out.Len())
			}
			switch kind {
			case "decoded-arrays":
				var limit *chunkclient.DecodeLimitError
				if !errors.As(err, &limit) {
					t.Fatal(err)
				}
			case "schema":
				var server *chunkclient.ServerError
				if !errors.As(err, &server) || server.Code != "SCHEMA_MISMATCH" {
					t.Fatal(err)
				}
			case "output":
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatal(err)
				}
			}
			if _, err := client.Do("PING"); err != nil {
				t.Fatal("local refusal closed connection", err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
