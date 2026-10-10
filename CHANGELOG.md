# Changelog

All notable changes to this project will be documented in this file.

This CLI follows [Semantic Versioning](https://semver.org/). Version 1.x
speaks the `chunkdb` 1.x protocol, version 2.x speaks protocol 3 (chunkdb
2.0); see the engine's
[compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

## Unreleased

### Breaking
- Protocol 3 (chunkdb 2.0): the CLI sends CQL statements. `chunk-cli
  [options] "<statement>"` runs one statement, `shell` reads one per line.
  Every connection opens with `HELLO 3`; a server of an earlier protocol is
  reported as such
- the commands of protocol 2 and 1.x are removed: `get`, `set`, `unset`,
  `mset`, `mget`, `chunk*`, `info`, `walflush`, `metrics`, the table
  commands, `use`, `auth`, and the `--table` option. Their statements are
  `GET` / `SET` / `DELETE BLOCK`, `GET` / `SET CHUNK`, `GET AREA`,
  `SCAN CHUNKS`, `FLUSH WAL`, `SHOW METRICS`, `CREATE` / `ALTER` /
  `DROP TABLE`, `SHOW TABLES` and `DESCRIBE`; the shell's `quit` now exits
  locally like `exit`
- bit-string block values, bit-text chunks and zrle transfer are removed
- users replace the token: the CLI logs in with a user and password
  (SCRAM-SHA-256), from `chunk://user:password@host/` (percent-decoded),
  `--user`, `--password-file`, `CHUNKDB_PASSWORD` or a prompt without echo
  on a terminal; without a user it logs in without one (`--auth none`
  servers). A server that cannot prove it knows the password fails the
  connection. `--token` and the token in the URI are removed

### Fixed
- Ctrl-C sends UNWATCH while watch output or a schema lookup is blocked; changes printed after cancellation begins are not acknowledged
- final ACK and UNWATCH write failures are reported instead of the resulting stream read error
- `--ack-every` without `--slot` reports the missing requirement

### Added
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

## 1.2.0 - 2026-09-03

### Added
- `chunksetbin` and `chunksetbinstate`: binary chunk writes over the new
  `CHUNKSETBIN` command (chunkdb server 1.3+). The payload is given as hex or
  read from a file with `--in`, in the byte layouts `chunkbin` /
  `chunkbinstate` print, so `chunkbin --out` output can be written back as is.
  Also available in `shell`

## 1.1.0 - 2026-07-18

### Added
- World-read commands: `chunkscan`, `chunkrange`, `chunkradius`.
- Chunk concurrency commands: `chunkver`, `chunkcas`, `chunkbatch`.
- `walflush` explicit durability barrier command.
- `chunkbinc` / `chunkbincstate` zrle-compressed binary chunk transfer with a
  bounded local decoder.

Each new command validates its arguments before sending. Existing commands and
their output are unchanged.

## 1.0.0

Initial stable release: point and chunk-level commands (`get`/`set`/`exists`/
`unset`, `chunkexists`/`chunk`/`chunkset`/`chunkbin` and their `state` forms),
`mset`/`mget` batch commands, `info`, `metrics`, and the interactive `shell`.
