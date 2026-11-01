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
	"strings"
	"testing"
	"time"
)

func TestChunkModelRawReplacementAfterQuotaReduction(t *testing.T) {
	form := outputModelForm(t)
	form = append(append(append([]byte{}, form[:111]...), bytes.Repeat([]byte{'x'}, 512)...), form[111:]...)
	binary.LittleEndian.PutUint32(form[68:], 524)
	binary.LittleEndian.PutUint32(form[76:], 623)
	binary.LittleEndian.PutUint32(form[88:], 660)
	binary.LittleEndian.PutUint32(form[107:], 512)
	file := filepath.Join(t.TempDir(), "chunk.bin")
	if err := os.WriteFile(file, form, 0600); err != nil {
		t.Fatal(err)
	}
	statement := `SET CHUNK 0 0 0 IN world $1`
	uri, done := rawAdmissionServer(t, func(conn net.Conn, r *bufio.Reader) error {
		if err := expectSlotLine(r, "DESCRIBE world"); err != nil {
			return err
		}
		describe := strings.ReplaceAll(modelDescribeWire(), watchWireBulk("text(8)"), watchWireBulk("text(1024)"))
		describe = strings.Replace(describe, watchWireMap("var_max_chunk_bytes", ":32\r\n"), watchWireMap("var_max_chunk_bytes", ":1\r\n"), 1)
		if _, err := io.WriteString(conn, describe); err != nil {
			return err
		}
		if err := expectSlotLine(r, statement); err != nil {
			return err
		}
		if err := expectSlotLine(r, fmt.Sprintf("$%d", len(form))); err != nil {
			return err
		}
		body := make([]byte, len(form)+2)
		if _, err := io.ReadFull(r, body); err != nil {
			return err
		}
		if !bytes.Equal(body, append(append([]byte{}, form...), 13, 10)) {
			return fmt.Errorf("changed import bytes")
		}
		_, err := io.WriteString(conn, ":10\r\n")
		return err
	})
	client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var out bytes.Buffer
	if err := execute(client, nil, statement, statementOptions{in: file}, &out, console{}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "10\n" {
		t.Fatal(out.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func modelDescribeWire() string {
	column := func(name, typ string, id int, nullable bool) string {
		return watchWireMap("id", fmt.Sprintf(":%d\r\n", id), "name", watchWireBulk(name), "type", watchWireBulk(typ), "null", fmt.Sprintf("#%c\r\n", map[bool]byte{true: 't', false: 'f'}[nullable]), "required", "#f\r\n", "default", "_\r\n")
	}
	return watchWireMap("table", watchWireBulk("world"), "epoch", watchWireBulk(watchEpoch), "revision", ":9\r\n", "dims", ":3\r\n", "version", ":7\r\n", "chunk", "*3\r\n:2\r\n:1\r\n:1\r\n", "large", "*3\r\n:4\r\n:4\r\n:4\r\n", "options", watchWireMap("var_max_chunk_bytes", ":32\r\n"), "columns", "*2\r\n"+column("n", "u8", 1, false)+column("text", "text(8)", 2, true), "chunk_columns", "*2\r\n"+column("flag", "bool", 1, false)+column("label", "text(8)", 2, true))
}

func TestChunkModelReadWire(t *testing.T) {
	form := outputModelForm(t)
	get := `GET CHUNK -1 2 0 FROM world`
	scan := `SCAN CHUNKS FROM world SINCE 3 WITH DATA`
	uri, done := rawAdmissionServer(t, func(conn net.Conn, r *bufio.Reader) error {
		for _, statement := range []string{get, scan} {
			if err := expectSlotLine(r, "DESCRIBE world"); err != nil {
				return err
			}
			if err := expectSlotLine(r, statement); err != nil {
				return err
			}
			if _, err := io.WriteString(conn, modelDescribeWire()); err != nil {
				return err
			}
			reply := watchWireBulk(string(form))
			if statement == scan {
				reply = watchWireMap("chunks", "*2\r\n*5\r\n:-1\r\n:2\r\n:0\r\n:9\r\n"+watchWireBulk(string(form))+"*5\r\n:0\r\n:0\r\n:1\r\n:10\r\n_\r\n", "more", "#f\r\n", "epoch", watchWireBulk(watchEpoch), "position", ":10\r\n", "retained_deletion_floor", ":3\r\n")
			}
			if _, err := io.WriteString(conn, reply); err != nil {
				return err
			}
		}
		return nil
	})
	client, err := connect(globalOptions{URI: uri, Timeout: 2 * time.Second}, console{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	for _, statement := range []string{get, scan} {
		var out bytes.Buffer
		if err := execute(client, nil, statement, statementOptions{json: true, blocks: true}, &out, console{}); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), `"chunk_columns":{"flag":true,"label":null}`) || !strings.Contains(out.String(), `"z":0`) {
			t.Fatal(out.String())
		}
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestChunkModelInvalidRawFormNeverSendsMutation(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chunk.bin")
	if err := os.WriteFile(file, []byte("not a checked form"), 0600); err != nil {
		t.Fatal(err)
	}
	uri, done := rawAdmissionServer(t, func(conn net.Conn, r *bufio.Reader) error {
		if err := expectSlotLine(r, "DESCRIBE world"); err != nil {
			return err
		}
		if _, err := io.WriteString(conn, modelDescribeWire()); err != nil {
			return err
		}
		if err := expectSlotLine(r, "PING"); err != nil {
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
	err = execute(client, nil, `SET CHUNK 0 0 0 IN world $1 IF ABSENT`, statementOptions{in: file}, &out, console{})
	if err == nil || out.Len() != 0 || client.Broken() != nil {
		t.Fatal(err, out.String())
	}
	if _, err := client.Do("PING"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
