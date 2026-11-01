package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func admissionHello() string {
	return watchWireMap("protocol", ":3\r\n", "server_version", watchWireBulk("2.0"), "max_line_bytes", ":1048576\r\n", "max_parameters", ":1024\r\n", "max_area_chunks", ":4096\r\n", "max_response_bytes", ":67108864\r\n", "max_scan_limit", ":4096\r\n", "server_signature", "_\r\n")
}
func admissionColumn(name, typ string, id int) string {
	return watchWireMap("id", fmt.Sprintf(":%d\r\n", id), "name", watchWireBulk(name), "type", watchWireBulk(typ), "null", "#f\r\n", "required", "#f\r\n", "default", "_\r\n")
}
func admissionDescribe(epoch string, version int, cols string, count int, dims int) string {
	pairs := []string{"table", watchWireBulk("world"), "version", fmt.Sprintf(":%d\r\n", version), "chunk", "*2\r\n:2\r\n:2\r\n", "large", "*2\r\n:4\r\n:4\r\n", "options", watchWireMap("var_max_chunk_bytes", ":32\r\n"), "columns", fmt.Sprintf("*%d\r\n", count) + cols}
	if epoch != "" {
		pairs = append(pairs, "epoch", watchWireBulk(epoch), "revision", ":0\r\n", "dims", fmt.Sprintf(":%d\r\n", dims), "chunk_columns", "*0\r\n")
	}
	return watchWireMap(pairs...)
}
func rawAdmissionServer(t *testing.T, flow func(net.Conn, *bufio.Reader) error) (string, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	t.Cleanup(cancel)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	done := make(chan error, 1)
	go func() {
		defer listener.Close()
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		deadline, _ := ctx.Deadline()
		conn.SetDeadline(deadline)
		r := bufio.NewReader(conn)
		if err = expectSlotLine(r, "HELLO 3"); err == nil {
			_, err = io.WriteString(conn, admissionHello())
		}
		if err == nil {
			err = flow(conn, r)
		}
		done <- err
	}()
	return "chunk://" + listener.Addr().String() + "/", done
}
func TestRawChunkTableRecognition(t *testing.T) {
	for _, tc := range []struct{ s, table string }{
		{"SET CHUNK 0 0 IN world $1", "world"}, {"set chunk -2 4 in world $1 IF VERSION 9", "world"}, {"SET CHUNK 0 0 IN \"my \"\"in\"\" table\" $1", "\"my \"\"in\"\" table\""},
		{"SET CHUNK 0 0 IN world $01", "world"}, {"SET CHUNK 0 0 IN world$1", "world"}, {"SET CHUNK 0-1IN world$01IF VERSION 9", "world"},
		{"SET CHUNK 0 0 IN world $1 IF VERSION -0", "world"}, {"SET CHUNK 0 0 IN world$01IF VERSION-00", "world"},
		{"SET CHUNK 0 0 IN world $1 IF VERSION 000", "world"}, {"SET CHUNK 0 0 IN world $1 IF VERSION 18446744073709551615", "world"},
		{"SET CHUNK 0 0 IN world $1 IF VERSION -0000000000000000000000000000000000000000", "world"},
		{"SET CHUNK 0 0 IN \"my \"\"in\"\" table\"$01", "\"my \"\"in\"\" table\""},
		{"SET BLOCK 0 0 IN world name = 'it''s SET CHUNK IN other', blob = $1", ""}, {"SET CHUNK 0 0 \"IN\" world $1", ""}, {"CREATE USER alice VERIFIER $1", ""}, {"SET CHUNK 0 0 IN world '$1'", ""},
		{"SET CHUNK 0 0 IN world $2", ""}, {"SET CHUNK 0 0 IN world $0", ""}, {"SET CHUNK 0 0 IN world $18446744073709551616", ""},
		{"SET CHUNK 0 0 IN world $", ""}, {"SET CHUNK 0 0 IN world $-1", ""}, {"SET CHUNK 0 0 IN world $1$2", ""}, {"SET CHUNK 0 0 IN world $1x", ""},
		{"SET CHUNK 0 0 IN world $1 IF VERSION -1", ""}, {"SET CHUNK 0 0 IN world $1 IF VERSION 18446744073709551616", ""},
		{"SET CHUNK 0 0 IN world $1 IF VERSION +0", ""}, {"SET CHUNK 0 0 IN world $1 IF VERSION 0.0", ""}, {"SET CHUNK 0 0 IN world $1 IF VERSION 0e0", ""},
		{"SET CHUNK 0 0 IN world $1 IF VERSION '0'", ""}, {"SET CHUNK 0 0 IN world $1 IF VERSION x'00'", ""}, {"SET CHUNK 0 0 IN world $1 IF VERSION b'0'", ""},
	} {
		got, ok := rawChunkTable(tc.s)
		if got != tc.table || ok != (tc.table != "") {
			t.Fatalf("%q: %q %t", tc.s, got, ok)
		}
	}
}

func TestChunkModelRawImportShape(t *testing.T) {
	for _, statement := range []string{
		`SET CHUNK -1 2 0 IN world $1 IF ABSENT`,
		`SET CHUNK -1 2 -3 IN "my table" COLUMNS "with", "a""b" $01 IF VERSION 9`,
		`SET CHUNK 0 0 IN world COLUMNS a $1 IF ABSENT`,
	} {
		shape, ok := rawChunkShapeOf(statement)
		if !ok || shape.table == "" {
			t.Fatal("unbounded chunk mutation", statement)
		}
		if strings.Contains(statement, `"my table"`) && (shape.dims != 3 || strings.Join(shape.columns, ",") != "with,a\"b") {
			t.Fatal(shape)
		}
	}
	for _, statement := range []string{
		`SET CHUNK 0 0 0 0 IN world $1`,
		`SET CHUNK 0 0 0 IN world META owner=$1`,
		`SET CHUNK 0 0 IN world COLUMNS $1`,
		`SET CHUNK 0 0 IN world $1 IF ABSENT IF VERSION 1`,
	} {
		if shape, ok := rawChunkShapeOf(statement); ok {
			t.Fatal("non-form import admitted", statement, shape)
		}
	}
}

func TestRawImportInvalidParametersKeepGenericValidation(t *testing.T) {
	for _, parameter := range []string{"$2", "$1x", "$1$3", "$", "'$1'", "$1 IF VERSION -1", "$1 IF VERSION 18446744073709551616", "$1 IF VERSION 0.0", "$1 IF VERSION '0'", "$1 IF VERSION x'00'"} {
		t.Run(parameter, func(t *testing.T) {
			statement := "SET CHUNK 0 0 IN world " + parameter
			data := []byte{0, 255, 13, 10}
			file := filepath.Join(t.TempDir(), "input.bin")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			framed := parameter == "$2" || parameter == "$1x" || strings.HasPrefix(parameter, "$1 IF VERSION")
			uri, done := rawAdmissionServer(t, func(conn net.Conn, r *bufio.Reader) error {
				if framed {
					// A non-chunk parameter stays on the original Do validation path.
					if err := expectSlotLine(r, statement); err != nil {
						return err
					}
					if err := expectSlotLine(r, fmt.Sprintf("$%d", len(data))); err != nil {
						return err
					}
					body := make([]byte, len(data)+2)
					if _, err := io.ReadFull(r, body); err != nil {
						return err
					}
					if !bytes.Equal(body, append(append([]byte{}, data...), 13, 10)) {
						return fmt.Errorf("generic parameter changed: %x", body)
					}
					io.WriteString(conn, "-ERR SYNTAX invalid parameter\r\n")
				}
				// Gap, absent and quoted parameters fail request framing locally.
				// Neither path may send DESCRIBE or break the ordinary connection.
				if err := expectSlotLine(r, "PING"); err != nil {
					return err
				}
				io.WriteString(conn, "+PONG\r\n")
				line, err := r.ReadString('\n')
				if line != "" || !errors.Is(err, io.EOF) {
					return fmt.Errorf("later request %q: %v", line, err)
				}
				return nil
			})
			client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var out bytes.Buffer
			err = execute(client, nil, statement, statementOptions{in: file}, &out, console{})
			want := "the statement names"
			if framed {
				want = "SYNTAX invalid parameter"
			}
			if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 || client.Broken() != nil {
				t.Fatalf("generic validation: %v; output %q; broken %v", err, out.String(), client.Broken())
			}
			if _, err := client.Do("PING"); err != nil {
				t.Fatal(err)
			}
			client.Close()
			waitSlotServer(t, done)
		})
	}
}

func rawImportSpelling(mode, table string) string {
	switch mode {
	case "leading-zero", "out-leading-zero", "known-leading-zero":
		return "SET CHUNK 0 0 IN " + table + " $01"
	case "adjacent", "transaction-adjacent", "known-adjacent", "quoted-adjacent":
		return "SET CHUNK 0 0 IN " + table + "$1"
	case "coordinate-in", "known-coordinate-in":
		return "SET CHUNK 0-1IN " + table + "$01"
	case "parameter-if", "known-parameter-if":
		return "SET CHUNK 0 0 IN " + table + "$01IF VERSION 9"
	case "negative-zero", "out-negative-zero", "transaction-negative-zero", "known-negative-zero":
		return "SET CHUNK 0 0 IN " + table + " $1 IF VERSION -0"
	case "negative-zeros", "known-negative-zeros":
		return "SET CHUNK 0 0 IN " + table + " $1 IF VERSION -00"
	case "negative-zero-adjacent", "known-negative-zero-adjacent":
		return "SET CHUNK 0 0 IN " + table + "$01IF VERSION-00"
	default:
		return "SET CHUNK 0 0 IN " + table + " $1"
	}
}

func TestRawImportRefusesBeforeMutation(t *testing.T) {
	for _, mode := range []string{"ordinary", "out", "quoted", "transaction", "oversize", "dimension", "password", "leading-zero", "adjacent", "coordinate-in", "parameter-if", "out-leading-zero", "transaction-adjacent", "quoted-adjacent", "negative-zero", "negative-zeros", "negative-zero-adjacent", "out-negative-zero", "transaction-negative-zero"} {
		t.Run(mode, func(t *testing.T) {
			transaction := strings.HasPrefix(mode, "transaction")
			output := strings.HasPrefix(mode, "out")
			file := filepath.Join(t.TempDir(), "chunk.bin")
			data := []byte{0, 1, 13, 10, 255}
			if mode == "oversize" {
				data = bytes.Repeat([]byte{7}, 64*1024*1024+200)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			table := "world"
			if strings.HasPrefix(mode, "quoted") {
				table = "\"my \"\"in\"\" table\""
			}
			statement := rawImportSpelling(mode, table)
			cols := admissionColumn("id", "u8", 1) + admissionColumn("future_payload", "vector(7)", 2)
			count, dims := 2, 2
			if mode == "oversize" || mode == "dimension" {
				cols = admissionColumn("id", "u8", 1)
				count = 1
			}
			if mode == "dimension" {
				dims = 3
			}
			uri, done := rawAdmissionServer(t, func(conn net.Conn, r *bufio.Reader) error {
				if transaction {
					if err := expectSlotLine(r, "BEGIN"); err != nil {
						return err
					}
					io.WriteString(conn, "+OK\r\n")
				}
				if mode != "password" {
					if err := expectSlotLine(r, "DESCRIBE "+table); err != nil {
						return err
					}
					io.WriteString(conn, admissionDescribe(watchEpoch, 1, cols, count, dims))
				}
				// This command gate proves that neither SET nor a parameter frame preceded PING.
				if err := expectSlotLine(r, "PING"); err != nil {
					return err
				}
				io.WriteString(conn, "+PONG\r\n")
				if transaction {
					if err := expectSlotLine(r, "ROLLBACK"); err != nil {
						return err
					}
					io.WriteString(conn, "+OK\r\n")
				}
				line, err := r.ReadString('\n')
				if !errors.Is(err, io.EOF) || line != "" {
					return fmt.Errorf("later frame %q: %v", line, err)
				}
				return nil
			})
			client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var tx *shellTxn
			var out bytes.Buffer
			if transaction {
				tx = &shellTxn{}
				if err = execute(client, tx, "BEGIN", statementOptions{}, &out, console{}); err != nil {
					t.Fatal(err)
				}
				out.Reset()
			}
			opts := statementOptions{in: file}
			outputFile := filepath.Join(t.TempDir(), "out.bin")
			if output {
				opts.out = outputFile
			}
			if mode == "password" {
				statement = "CREATE USER alice PASSWORD"
			}
			err = execute(client, tx, statement, opts, &out, console{})
			want := "future_payload"
			if mode == "oversize" {
				want = "schema size bound"
			}
			if mode == "dimension" {
				want = "expected 3 geometry axes"
			}
			if mode == "password" {
				want = "--in cannot be used with PASSWORD"
			}
			if err == nil || !strings.Contains(err.Error(), want) || out.Len() != 0 || client.Broken() != nil {
				t.Fatalf("refusal: %v, output %q, broken %v", err, out.String(), client.Broken())
			}
			if want == "future_payload" && !strings.Contains(err.Error(), "vector(7)") {
				t.Fatal("lost type", err)
			}
			if output {
				if _, err := os.Stat(outputFile); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("created output on refusal", err)
				}
			}
			if tx != nil && !tx.open {
				t.Fatal("local refusal ended transaction")
			}
			if _, err := client.Do("PING"); err != nil {
				t.Fatal("refusal broke live connection", err)
			}
			if tx != nil {
				if err = execute(client, tx, "ROLLBACK", statementOptions{}, &out, console{}); err != nil || tx.open {
					t.Fatal("rollback", err)
				}
			}
			client.Close()
			waitSlotServer(t, done)
		})
	}
}
func knownAdmissionForm() []byte {
	var data []byte
	for i, value := range [][]byte{[]byte("a\x00b"), {0, 255, 13, 10}} {
		header := make([]byte, 12)
		binary.LittleEndian.PutUint32(header, uint32(i+1))
		binary.LittleEndian.PutUint32(header[8:], uint32(len(value)))
		data = append(append(data, header...), value...)
	}
	return checkedWireFixture(1, 9, []uint16{1, 4, 6}, [][]byte{{1}, data, {0, 0, 0, 0}})
}

func TestRawImportExportKnownBytesAndUnrelatedParameters(t *testing.T) {
	for _, mode := range []string{"known", "transaction", "export", "generic", "verifier", "known-leading-zero", "known-adjacent", "known-coordinate-in", "known-parameter-if", "known-negative-zero", "known-negative-zeros", "known-negative-zero-adjacent"} {
		t.Run(mode, func(t *testing.T) {
			data := knownAdmissionForm()
			file := filepath.Join(t.TempDir(), "form.bin")
			os.WriteFile(file, data, 0600)
			statement := rawImportSpelling(mode, "world")
			if mode == "export" {
				statement = "GET CHUNK 0 0 FROM world"
			}
			if mode == "generic" {
				statement = "SET BLOCK 0 0 IN world blob = $1"
			}
			if mode == "verifier" {
				statement = "CREATE USER alice VERIFIER $1"
			}
			uri, done := rawAdmissionServer(t, func(conn net.Conn, r *bufio.Reader) error {
				if mode == "transaction" {
					if err := expectSlotLine(r, "BEGIN"); err != nil {
						return err
					}
					io.WriteString(conn, "+OK\r\n")
				}
				if strings.HasPrefix(mode, "known") || mode == "transaction" {
					if err := expectSlotLine(r, "DESCRIBE world"); err != nil {
						return err
					}
					io.WriteString(conn, admissionDescribe(watchEpoch, 1, admissionColumn("label", "text(8)", 1)+admissionColumn("blob", "bytes(8)", 2), 2, 2))
				}
				if err := expectSlotLine(r, statement); err != nil {
					return err
				}
				if mode == "export" {
					io.WriteString(conn, watchWireBulk(string(data)))
				} else {
					if err := expectSlotLine(r, fmt.Sprintf("$%d", len(data))); err != nil {
						return err
					}
					body := make([]byte, len(data)+2)
					if _, err := io.ReadFull(r, body); err != nil {
						return err
					}
					if !bytes.Equal(body, append(append([]byte{}, data...), 13, 10)) {
						return fmt.Errorf("raw bytes changed %x", body)
					}
					if mode == "transaction" {
						io.WriteString(conn, "_\r\n")
					} else {
						io.WriteString(conn, ":9\r\n")
					}
				}
				if mode == "transaction" {
					if err := expectSlotLine(r, "ROLLBACK"); err != nil {
						return err
					}
					io.WriteString(conn, "+OK\r\n")
				}
				line, err := r.ReadString('\n')
				if !errors.Is(err, io.EOF) || line != "" {
					return fmt.Errorf("later request %q: %v", line, err)
				}
				return nil
			})
			client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			var tx *shellTxn
			var out bytes.Buffer
			if mode == "transaction" {
				tx = &shellTxn{}
				if err = execute(client, tx, "BEGIN", statementOptions{}, &out, console{}); err != nil {
					t.Fatal(err)
				}
				out.Reset()
			}
			opts := statementOptions{in: file}
			if mode == "export" {
				opts = statementOptions{out: file}
			}
			if err = execute(client, tx, statement, opts, &out, console{}); err != nil {
				t.Fatal(err)
			}
			if mode == "export" {
				got, err := os.ReadFile(file)
				if err != nil || !bytes.Equal(got, data) {
					t.Fatalf("raw export changed %x: %v", got, err)
				}
			} else if mode == "transaction" {
				if out.String() != "(applies at COMMIT)\n" || !tx.open {
					t.Fatal("transaction write", out.String())
				}
				if err = execute(client, tx, "ROLLBACK", statementOptions{}, &out, console{}); err != nil {
					t.Fatal(err)
				}
			} else if out.String() != "9\n" {
				t.Fatal(out.String())
			}
			client.Close()
			waitSlotServer(t, done)
		})
	}
}

// checkedWireFixture writes independent directory records for wire fixtures.
func checkedWireFixture(schema, version uint64, kinds []uint16, bodies [][]byte) []byte {
	data := make([]byte, 36+12*len(kinds))
	copy(data, "CKCF")
	data[4] = 1
	binary.LittleEndian.PutUint64(data[8:], version)
	binary.LittleEndian.PutUint64(data[16:], schema)
	binary.LittleEndian.PutUint16(data[32:], uint16(len(kinds)))
	for i, kind := range kinds {
		at := 36 + 12*i
		binary.LittleEndian.PutUint16(data[at:], kind)
		binary.LittleEndian.PutUint32(data[at+4:], uint32(len(data)))
		binary.LittleEndian.PutUint32(data[at+8:], uint32(len(bodies[i])))
		data = append(data, bodies[i]...)
	}
	return data
}
