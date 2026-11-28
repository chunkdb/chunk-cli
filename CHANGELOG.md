# Changelog

All notable changes to this project will be documented in this file.

The current CLI uses chunkdb 2.0 protocol 3; see the server’s [compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

## Unreleased

### Changed
- `watch --catalog` streams WATCH CATALOG with one line per table, user and slot change; `DESCRIBE` prints a table's `checks` and `fenced` reason; `SHOW STATUS`, `SHOW TABLE STATS` and `SHOW SLOTS` check the types of their 2.0 fields; COMMANDS.md lists the CLI form of every statement. Part of chunkdb/chunkdb#151.
- Ctrl-C during a one-shot statement sends CANCEL for it from a second connection; `--tls-ca <file>` verifies `chunks://` servers with the PEM CA certificates in the file. Part of chunkdb/chunkdb#151.
- `GET AREA ... SCALE` chunks print their scale and reduction and count samples; `SET BLOCK` and `DELETE BLOCK ... RETURNING` print typed rows; transaction files may open with `BEGIN READ ONLY`. Part of chunkdb/chunkdb#151.
- `SCAN CHUNKS ... WHERE ... WITH DATA` prints the page's `after` coordinate and accepts empty filtered pages; tests cover IF conditions, computed values and WHERE on AGGREGATE and SCAN CHUNKS. Part of chunkdb/chunkdb#151.
- `watch --area` may repeat to watch several areas with the same axes in one stream. Part of chunkdb/chunkdb#151.
- `--in <n>=<file>` supplies the binary parameter `$n`, and `--in` may repeat; `--json` errors carry the server's `reason`; exit codes are tested for every server code. Part of chunkdb/chunkdb#151.
- `watch` takes its table from the URI path when none is named, as in `chunk://host:4242/world`. Part of chunkdb/chunkdb#151.
- Connection, URI and login errors name the next step: the `chunk://user@host:4242/` form, `chunk://` versus `chunks://` after a reset, and the password sources after `AUTH_FAILED`. Part of chunkdb/chunkdb#152.
- Render GET AREA pages with exact continuation cursors, enforce negotiated reply bounds while reading, and bound aggregate decoded output arrays (chunkdb/chunkdb#136).
- Decode checked chunk forms with three-axis coordinates, chunk columns, producing timestamps and retained scan deletions, while bounding decoded output and binary imports (chunkdb/chunkdb#133).
- Bind logins over `chunks://` to their TLS connection (`tls-exporter` on TLS 1.3, `tls-server-end-point` on TLS 1.2); `--channel-binding auto|off|require` selects the mode, and `CREATE USER <name> PASSWORD REQUIRE CHANNEL BINDING` is passed on. Part of chunkdb/chunkdb#162.
- `CREATE SLOT ... OWNER <user>` sets a slot's owner, and `SHOW SLOTS` validates and prints its nullable `owner`.
- Decode extensible protocol metadata and feed events, negotiate HELLO options, classify errors by code, and preserve exact integers in JSON. Part of chunkdb/chunkdb#69.
- `ALTER USER <name> ADD PASSWORD` adds a second password with the salt of the current one, from a login as that user, for rotation with DROP OLD VERIFIER; `CREATE USER IF NOT EXISTS <name> PASSWORD` computes the verifier too. Part of chunkdb/chunkdb#151.
- `SCAN CHUNKS FROM table AREA ...` and `AREA AROUND ...` print typed `WITH DATA` pages and refuse a chunk outside the area. Part of chunkdb/chunkdb#151.

### Documentation
- Follow the server's rights and statements: BACKUP TO takes `BACKUP` or `MANAGES USERS` and RESTORE TABLE restores one table online, `migrations` lists the records within your rights, transactions list every statement they allow, and the shell rolls back a conflict itself. Part of chunkdb/chunkdb#151.
- CONNECTION.md says `statement_timeout_ms` is applied, COMMANDS.md says `--uri` defaults to `chunk://127.0.0.1:4242/`, and the watch synopsis lists `--columns`, `--no-before`, `--chunks`, `--takeover`, `--ack-timeout-ms` and `--no-reconnect`. Part of #45.
- Add a checked AGGREGATE example with a BY CHUNK JSON reply to the command reference. Part of chunkdb/chunkdb#151.
- Replace the README start with a checked world example: a named migration for `world_cli`, two writes, block and area reads, and a watched change; `examples/world.sh` applies the same migration, so it reruns cleanly and does not clash with the server quick start's `world` table. Part of chunkdb/chunkdb#152.
- Clarify CLI commands and user guides with checked input and output examples (#6).
- Correct the world example description and link feed recovery to the server procedure (#6).
- Rewrite the CLI README and user guides for chunkdb 2.0, with a short start and runnable feature examples (#6).

### Breaking
- The Go module path is `github.com/chunkdb/chunk-cli/v2` for 2.x; 1.x installs keep `github.com/chunkdb/chunk-cli/cmd/chunk-cli@<1.x-version>`
- Use chunkdb 2.0 protocol 3: one CQL statement per invocation or line in the shell, with HELLO 3 on each connection.
- Typed chunk forms carry schema versions; users authenticate with passwords through SCRAM-SHA-256.

### Fixed
- A connection lost or timed out after a statement was sent exits 5, the `unknown_outcome` status, and `--json` reports class `unknown_outcome`, since the statement may have applied; inside a transaction only `COMMIT` does, because the server rolls back the statements before it. A lost login still exits 1. Part of #45.
- Transaction-file retries wait the error's `retry_after_ms`, at most 10 seconds, when it has one, and `reread` errors (`VERSION_MISMATCH`, `CONDITION_FAILED`, `SCHEMA_MISMATCH`) are not retried, since the same statements fail the same way. Part of #45.
- A watch reconnect never sends `TAKEOVER`: only the first attach takes the slot over when `--takeover` is given, and `SLOT_BUSY` on a reconnect ends the watch with that error. Part of #45.
- Reply lines are capped at 1 MiB, as in the Go library. Part of #45.
- User names are checked as `[a-z_][a-z0-9_]*` before login, so a name with `$`, `'` or `"` fails before connecting instead of changing HELLO. Part of #45.
- A URI that does not parse no longer repeats its password on stderr or in `--json` output; the error shows the URI with the password replaced by `xxxxx`. Part of #45.
- A login refuses a server-first message whose iteration count is outside 4096 to 1000000, the range a chunkdb server issues. Part of #45.
- `watch --area` accepts six inclusive bounds for three-axis tables, `cx0,cy0,cz0,cx1,cy1,cz1`, and refuses any other count
- `watch --columns` decodes rows in the requested column order from the first change; within a table epoch the projection follows column IDs across renames, and a reconnect requests the projected columns by their current names.
- A DESCRIBE chunk or large-chunk dimension of 2147483648 is refused as an invalid size, as larger ones already were; a chunk form entry whose block or length does not fit an `int` is refused on 32-bit platforms.
- Print never-written chunks as (null), or JSON null, while retaining versioned empty forms and refusing NULL binary exports. (#6).
- Connection errors explain how to check a refused endpoint, TLS mismatch or timeout while preserving the underlying error
- Raw MIGRATE statements reach the server when the keyword is not lowercase or is followed by a quoted name; malformed file markers report their line instead of merging steps
- Ctrl-C sends UNWATCH while watch output or a schema lookup is blocked; changes printed after cancellation begins are not acknowledged
- final ACK and UNWATCH write failures are reported instead of the resulting stream read error
- `--ack-every` without `--slot` reports the missing requirement

### Added
- Retry transaction files on known retryable errors and resume watches after connection loss, with bounded backoff and flags to disable retries or reconnection (#25).
- Table examples start from an empty server catalog; DESCRIBE includes per-table feed and durable-slot limits. (#6).
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
