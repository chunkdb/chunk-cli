# Changelog

All notable changes to this project will be documented in this file.

The current CLI uses chunkdb 2.0 protocol 3; see the server’s [compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

## Unreleased

### Documentation
- Rewrite the CLI README and user guides for chunkdb 2.0, with a short start and runnable feature examples (#6).

### Breaking
- Use chunkdb 2.0 protocol 3: one CQL statement per invocation or line in the shell, with HELLO 3 on each connection.
- Typed chunk forms carry schema versions; users authenticate with passwords through SCRAM-SHA-256.

### Fixed
- Connection errors explain how to check a refused endpoint, TLS mismatch or timeout while preserving the underlying error
- Raw MIGRATE statements reach the server when the keyword is not lowercase or is followed by a quoted name; malformed file markers report their line instead of merging steps
- Ctrl-C sends UNWATCH while watch output or a schema lookup is blocked; changes printed after cancellation begins are not acknowledged
- final ACK and UNWATCH write failures are reported instead of the resulting stream read error
- `--ack-every` without `--slot` reports the missing requirement

### Added
- `examples/world.sh` creates and fills a typed table, reads an area and watches an update; its integration test runs the script against a server
- `migrate <file>` applies named schema steps once, prints applied/skipped results and stops on the first error; `migrations` lists their history, with JSON output supported by both commands
- `watch <table> --slot <name>` resumes a durable slot and acknowledges only fully printed changes; `--ack-every <n>` batches acknowledgements (default 1), and Ctrl-C flushes a partial batch before UNWATCH. `CREATE SLOT`, `DROP SLOT` and `SHOW SLOTS` work as statements and in the shell
- `watch <table>` streams committed before/after changes, schema updates and resync positions, with inclusive chunk `--area`, `--after epoch:revision`, and NDJSON `--json`; Ctrl-C sends UNWATCH and waits for its acknowledgement
- replies print for people: a block as `column = value` lines, values as CQL
  literals (`NULL`, `'text'`, `x'bytes'`, `b'bits'`), maps as `key = value`
  lines, arrays as numbered rows; `GET CHUNK` and `GET AREA` print a summary
  of each chunk, decoded with the table's columns, and with `--blocks` the
  values of every present block
- `--json` prints each reply as one line of JSON
- `--out <file>` writes a reply's bytes (a `GET CHUNK` chunk form) to a file,
  `--in <file>` sends a file as the parameter `$1` of `SET CHUNK`; in the
  shell these options, `--json` and `--blocks` may start a line
- `CREATE USER <name> PASSWORD [MANAGES USERS]` and `ALTER USER <name>
  PASSWORD` read the new password from `--new-password-file <file>` or ask
  for it on the terminal, and send its verifier as `VERIFIER $1`
- `AUTH_REQUIRED` explains how to log in
- transactions in the shell: `BEGIN`, `COMMIT` and `ROLLBACK` run on the
  shell's connection and the prompt turns `chunk*>` while one is open;
  writes inside print `(applies at COMMIT)`, `COMMIT` prints the version of
  the written chunks, and a `CONFLICT` says the transaction ended without
  writing. A one-shot `BEGIN`, `COMMIT` or `ROLLBACK` fails and points to
  the shell

Historical release notes: [1.x and the protocol 3 transition](docs/releases/1.x.md).
