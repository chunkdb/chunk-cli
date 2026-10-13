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
		{"SET BLOCK 0 0 IN world name = 'it''s SET CHUNK IN other', blob = $1", ""}, {"SET CHUNK 0 0 \"IN\" world $1", ""}, {"CREATE USER alice VERIFIER $1", ""}, {"SET CHUNK 0 0 IN world '$1'", ""},
	} {
		got, ok := rawChunkTable(tc.s)
		if got != tc.table || ok != (tc.table != "") {
			t.Fatalf("%q: %q %t", tc.s, got, ok)
		}
	}
}
func TestRawImportRefusesBeforeMutation(t *testing.T) {
	for _, mode := range []string{"ordinary", "out", "quoted", "transaction", "oversize", "dimension", "password"} {
		t.Run(mode, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "chunk.bin")
			data := []byte{0, 1, 13, 10, 255}
			if mode == "oversize" {
				data = bytes.Repeat([]byte{7}, 100)
			}
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			table := "world"
			if mode == "quoted" {
				table = "\"my \"\"in\"\" table\""
			}
			statement := "SET CHUNK 0 0 IN " + table + " $1"
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
				if mode == "transaction" {
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
				if mode == "transaction" {
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
			if mode == "transaction" {
				tx = &shellTxn{}
				if err = execute(client, tx, "BEGIN", statementOptions{}, &out, console{}); err != nil {
					t.Fatal(err)
				}
				out.Reset()
			}
			opts := statementOptions{in: file}
			outputFile := filepath.Join(t.TempDir(), "out.bin")
			if mode == "out" {
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
				want = "dimensions 3"
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
			if mode == "out" {
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
	data := make([]byte, 17)
	binary.LittleEndian.PutUint64(data, 9)
	binary.LittleEndian.PutUint64(data[8:], 1)
	data[16] = 1
	for i, value := range [][]byte{[]byte("a\x00b"), {0, 255, 13, 10}} {
		header := make([]byte, 12)
		binary.LittleEndian.PutUint32(header, uint32(i+1))
		binary.LittleEndian.PutUint32(header[8:], uint32(len(value)))
		data = append(append(data, header...), value...)
	}
	return data
}

func TestRawImportExportKnownBytesAndUnrelatedParameters(t *testing.T) {
	for _, mode := range []string{"known", "transaction", "export", "generic", "verifier"} {
		t.Run(mode, func(t *testing.T) {
			data := knownAdmissionForm()
			file := filepath.Join(t.TempDir(), "form.bin")
			os.WriteFile(file, data, 0600)
			statement := "SET CHUNK 0 0 IN world $1"
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
				if mode == "known" || mode == "transaction" {
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
