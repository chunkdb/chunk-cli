package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestCLIAreaPages(t *testing.T) {
	for _, tls := range []bool{false, true} {
		for _, dims := range []int{2, 3} {
			t.Run(fmt.Sprintf("TLS=%t/dims=%d", tls, dims), func(t *testing.T) {
				s := startServerConfigured(t, tls, false, 2, "--max-area-chunks", "2", "--max-area-response-bytes", "256", "--txn-max-written-chunks", "1", "--txn-max-read-chunks", "2")
				third, depth := "", ""
				if dims == 3 {
					third, depth = " 0", " x 1"
				}
				s.ok(t, "CREATE TABLE t (v u8 DEFAULT 0) CHUNK 1 x 1"+depth+" CHUNK COLUMNS (tag u8 DEFAULT 7)")
				s.ok(t, "SET BLOCK 5 0"+third+" IN t v=9")
				selector := "GET AREA 0 0" + third + " TO 5 0" + third + " FROM t"
				read := func(statement string) map[string]any {
					t.Helper()
					var page map[string]any
					decoder := json.NewDecoder(strings.NewReader(s.ok(t, "--json", statement)))
					decoder.UseNumber()
					if err := decoder.Decode(&page); err != nil {
						t.Fatal(err)
					}
					return page
				}
				first := read(selector + " LIMIT 2")
				if first["more"] != true || len(first["chunks"].([]any)) != 0 || first["after"].([]any)[0] != json.Number("1") || len(first["after"].([]any)) != dims {
					t.Fatal(first)
				}
				compressed := "GET AREA-1-2 TO 0 0 FROM t LIMIT 1"
				if dims == 3 {
					compressed = "GET AREA-1-2-3 TO 0 0 0 FROM t LIMIT 1"
				}
				compact := read(compressed)
				if compact["more"] != true || len(compact["after"].([]any)) != dims || compact["after"].([]any)[0] != json.Number("-1") || compact["after"].([]any)[1] != json.Number("-2") {
					t.Fatal("compressed area prefix", compact)
				}
				last := read(selector + " COLUMNS v WITH SCHEMA AFTER 3 0" + third + " LIMIT 2")
				page := last["page"].(map[string]any)
				chunk := page["chunks"].([]any)[0].(map[string]any)
				if last["schema_version"] != json.Number("1") || page["more"] != false || page["after"] != nil || chunk["cx"] != json.Number("5") || chunk["projected"] != true || chunk["chunk_columns"].(map[string]any)["tag"] != json.Number("7") {
					t.Fatal(last)
				}
				if dims == 3 && chunk["cz"] != json.Number("0") {
					t.Fatal(chunk)
				}
				completed := read(selector + " AFTER 5 0" + third)
				if completed["more"] != false || completed["after"] != nil || len(completed["chunks"].([]any)) != 0 {
					t.Fatal(completed)
				}
				s.fails(t, "INVALID_ARGUMENT", selector)
				s.fails(t, "INVALID_ARGUMENT", selector+" LIMIT 3")
				s.fails(t, "INVALID_ARGUMENT", selector+" LIMIT 1 AFTER 6 0"+third)
				for x := 0; x < 2; x++ {
					s.ok(t, fmt.Sprintf("SET BLOCK %d 0%s IN t v=8", x, third))
				}
				short := read(selector + " LIMIT 2")
				if short["more"] != true || short["after"].([]any)[0] != json.Number("0") || len(short["chunks"].([]any)) != 1 {
					t.Fatal(short)
				}
				if output := s.ok(t, selector+" LIMIT 2 AFTER 0 0"+third); !strings.HasPrefix(output, "more = true\nafter = [2, 0") || !strings.Contains(output, "chunk 1 0") {
					t.Fatal(output)
				}
			})
		}
	}
}
