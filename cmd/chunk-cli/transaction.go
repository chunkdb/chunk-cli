package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/chunkdb/chunk-cli/internal/chunkclient"
)

// txnControl is a statement that starts or ends a transaction.
type txnControl string

const (
	txnNone     txnControl = ""
	txnBegin    txnControl = "BEGIN"
	txnCommit   txnControl = "COMMIT"
	txnRollback txnControl = "ROLLBACK"
)

// txnControlOf returns the transaction statement a statement starts with,
// or txnNone.
func txnControlOf(statement string) txnControl {
	tokens := tokenize(statement)
	if len(tokens) == 0 {
		return txnNone
	}
	switch control := txnControl(strings.ToUpper(tokens[0])); control {
	case txnBegin, txnCommit, txnRollback:
		return control
	}
	return txnNone
}

// isWrite reports whether a statement is SET BLOCK, SET CHUNK or DELETE
// BLOCK, which answer `_` inside a transaction.
func isWrite(statement string) bool {
	tokens := tokenize(statement)
	return len(tokens) > 0 && (strings.EqualFold(tokens[0], "SET") || strings.EqualFold(tokens[0], "DELETE"))
}

// shellTxn is the shell's view of the transaction on its connection. The
// server keeps one transaction per connection, so the shell follows the
// replies: a BEGIN that succeeds opens it; COMMIT and ROLLBACK end it
// whatever they answer, unless the statement did not parse; a CONFLICT
// ends it.
type shellTxn struct {
	open bool
}

// control runs BEGIN, COMMIT or ROLLBACK and prints its reply: OK, or the
// version COMMIT gave every written chunk.
func (t *shellTxn) control(client *chunkclient.Client, control txnControl, statement string, opts statementOptions, stdout io.Writer) error {
	if opts.in != "" || opts.out != "" {
		return fmt.Errorf("%s takes no --in or --out", control)
	}
	reply, err := client.Do(statement)
	var serverErr *chunkclient.ServerError
	switch {
	case err == nil:
		t.open = control == txnBegin
	case control != txnBegin && errors.As(err, &serverErr) && serverErr.Code != "SYNTAX":
		t.open = false
	}
	if err != nil {
		return err
	}
	if control == txnCommit && reply.Kind == chunkclient.KindNull && !opts.json {
		_, err = fmt.Fprintln(stdout, "(nothing written)")
		return err
	}
	return printReply(stdout, reply, opts)
}

// ended reports a CONFLICT, which ends the transaction without writing
// anything.
func (t *shellTxn) ended(err error) error {
	var serverErr *chunkclient.ServerError
	if errors.As(err, &serverErr) && serverErr.Code == "CONFLICT" {
		t.open = false
		return fmt.Errorf("%w (the transaction ended and wrote nothing; run it again from BEGIN)", err)
	}
	return err
}
