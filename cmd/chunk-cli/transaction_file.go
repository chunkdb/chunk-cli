package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/chunkdb/chunk-cli/v2/internal/chunkclient"
	"github.com/chunkdb/chunk-cli/v2/internal/chunkuri"
)

// transactionStatements snapshots the input before any request is sent.
func transactionStatements(source []byte) ([]string, error) {
	var statements []string
	scanner := bufio.NewScanner(bytes.NewReader(source))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "--") {
			continue
		}
		statements = append(statements, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read transaction: %w", err)
	}
	if len(statements) >= 2 && strings.EqualFold(statements[0], "BEGIN") && strings.EqualFold(statements[len(statements)-1], "COMMIT") {
		statements = statements[1 : len(statements)-1]
	}
	if len(statements) == 0 {
		return nil, errors.New("transaction file has no statements")
	}
	for _, statement := range statements {
		if txnControlOf(statement) != txnNone {
			return nil, errors.New("transaction file may contain only one outer BEGIN and COMMIT pair")
		}
		if _, _, password := passwordStatement(statement); password {
			return nil, errors.New("password statements do not run in a transaction file")
		}
	}
	return statements, nil
}

func runTransactionFile(ctx context.Context, opts globalOptions, args []string, term console, stdout io.Writer) error {
	if opts.Statement.out != "" || opts.Statement.newPasswordFile != "" || opts.Statement.blocks {
		return errors.New("transaction supports --json and --in; --out, --blocks and password options do not apply")
	}
	file := opts.Statement.in
	if len(args) == 1 && file == "" {
		file = args[0]
	} else if len(args) != 0 || file == "" {
		return errors.New("usage: chunk-cli [options] transaction <file>, or chunk-cli --in <file> transaction")
	}
	var source []byte
	var err error
	if file == "-" {
		source, err = io.ReadAll(term.in)
	} else {
		source, err = os.ReadFile(file)
	}
	if err != nil {
		return fmt.Errorf("read transaction %s: %w", file, err)
	}
	statements, err := transactionStatements(source)
	if err != nil {
		return err
	}
	uri, err := chunkuri.Parse(opts.URI)
	if err != nil {
		return err
	}
	login, err := loginFor(opts, uri, term)
	if err != nil {
		return err
	}
	limit := maxRetries
	if opts.NoRetry {
		limit = 0
	}
	for attempt := 0; ; attempt++ {
		var output bytes.Buffer
		client, err := connectWatchLogin(ctx, opts, uri, login)
		if err == nil {
			err = watchCall(ctx, client, func() error {
				txn := &shellTxn{}
				statementOpts := statementOptions{json: opts.Statement.json}
				if err := txn.control(client, txnBegin, "BEGIN", statementOpts, &output); err != nil {
					return err
				}
				for _, statement := range statements {
					if err := execute(client, txn, statement, statementOpts, &output, term); err != nil {
						return err
					}
				}
				return txn.control(client, txnCommit, "COMMIT", statementOpts, &output)
			})
			// Closing discards any open transaction before the next attempt.
			_ = client.Close()
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			_, err = io.Copy(stdout, &output)
			return err
		}
		var server *chunkclient.ServerError
		if !errors.As(err, &server) || server.RetryClass() != "rerun" && server.RetryClass() != "reread" && server.RetryClass() != "backoff" {
			return err
		}
		if attempt >= limit {
			failure := *server
			failure.Message = fmt.Sprintf("transaction gave up after %d attempt(s): %s", attempt+1, server.Message)
			return &failure
		}
		if err := retryPause(ctx, attempt); err != nil {
			return err
		}
	}
}
