package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInFlagNumbersParameters(t *testing.T) {
	var opts statementOptions
	for _, spec := range []string{"one.bin", "3=three.bin", "2=two.bin"} {
		if err := opts.addIn(spec); err != nil {
			t.Fatal(spec, err)
		}
	}
	files, err := opts.inFiles()
	if err != nil || strings.Join(files, ",") != "one.bin,two.bin,three.bin" {
		t.Fatal(files, err)
	}
	for _, spec := range []string{"1=again.bin", "2=again.bin", "0=zero.bin", "2="} {
		copied := opts
		if err := copied.addIn(spec); err == nil {
			t.Fatal("accepted", spec)
		}
	}
	gap := statementOptions{}
	_ = gap.addIn("1=a")
	_ = gap.addIn("3=c")
	if _, err := gap.inFiles(); err == nil || !strings.Contains(err.Error(), "$2") {
		t.Fatal(err)
	}
	for _, spec := range []string{"65536=x.bin", "4000000000=x.bin", "99999999999=x.bin"} {
		if err := (&statementOptions{}).addIn(spec); err == nil {
			t.Fatal("accepted", spec)
		}
	}
	if err := (&statementOptions{}).addIn("65535=x.bin"); err != nil {
		t.Fatal(err)
	}
	if err := (&statementOptions{}).addIn("a=b.bin"); err != nil {
		t.Fatal("a file name containing = must stay a $1 file", err)
	}
}

func TestInLineOptionReplacesGlobalFiles(t *testing.T) {
	defaults := statementOptions{in: "global.bin"}
	opts, statement, err := parseLineOptions("--in 2=b.bin --in a.bin ECHO $1 $2", defaults)
	if err != nil || statement != "ECHO $1 $2" || opts.in != "a.bin" || opts.inMore[2] != "b.bin" || defaults.in != "global.bin" || defaults.inMore != nil {
		t.Fatal(opts, statement, err)
	}
}

func TestInSendsEveryNumberedParameter(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first"), filepath.Join(dir, "second")
	if err := os.WriteFile(first, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	uri, done := fakeMigrationServer(t, []string{"ECHO $1 $2", "$5", "first", "$6", "second"}, []string{"", "", "", "", "+OK\r\n"})
	var out, failure bytes.Buffer
	if got := run([]string{"--uri", uri, "--in", first, "--in", "2=" + second, "ECHO $1 $2"}, strings.NewReader(""), &out, &failure); got != 0 {
		t.Fatal(got, out.String(), failure.String())
	}
	waitSlotServer(t, done)
}

func TestTransactionRefusesNumberedIn(t *testing.T) {
	var out, failure bytes.Buffer
	got := run([]string{"--uri", "chunk://127.0.0.1:1", "--no-retry", "--in", "t.cql", "--in", "2=x.bin", "transaction"}, strings.NewReader(""), &out, &failure)
	if got == 0 || !strings.Contains(failure.String(), "numbered --in") {
		t.Fatal(got, failure.String())
	}
}
