# Changelog

All notable changes to this project will be documented in this file.

This CLI follows [Semantic Versioning](https://semver.org/). Version 1.x
speaks the `chunkdb` 1.x protocol, version 2.x speaks protocol 2 (chunkdb
2.0); see the engine's
[compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

## Unreleased

### Breaking
- Protocol 2 (chunkdb 2.0). Every connection opens with `HELLO 2`, which
  carries the token and the table; a wrong or missing token or an unknown
  table fails the command, and a 1.x server is reported as such. The `auth`
  command (also in the shell) is removed
- `get` and `mget` print `(unset)` for a block without a value; `exists` is
  removed
- `chunk`, `chunkstate`, `chunkset` and `chunksetstate` keep their bit-text
  form but run over `CHUNKGET` / `CHUNKPUT` with the table's geometry;
  `chunkset` and `chunksetstate` print the chunk's version instead of `OK`
- `chunkget [--state] [--zrle] [--out <file>]` replaces `chunkbin`,
  `chunkbinstate`, `chunkbinc` and `chunkbincstate`; `chunkput [--state]
  [--zrle] [--if <version>]` (hex or `--in <file>`) replaces `chunksetbin`,
  `chunksetbinstate` and `chunkcas`
- `chunkbatch` takes `--if <version>` before the coordinates instead of a
  `<version|->` argument
- `chunkrange` / `chunkradius` print the same lines as before, built from the
  binary reply

### Added
- tables (chunkdb 2.0+): `tables`, `tableinfo`, `use`, `tablecreate`,
  `tableset` and `tabledrop`, in the shell as well; `--table <table>` or the
  URI path (`chunk://token@host:port/terrain`) selects the table every
  command works on. The shell prompt shows the selected table
  (`chunk:terrain>`)
- per-block extra data (chunkdb 2.0+): `xget [--bits]`, `xput` (`0`/`1` text, or bytes with `--hex` or `--in <file>` and `--bit-length <n>`) and `xdel`; `chunkget --extra` lists a chunk's values after the dump and `chunkput --extra` writes a state with its values; `chunkbatch` takes `XPUT <x> <y> <bits>` and `XDEL <x> <y>`. `tablecreate` and `tableset` pass `extra_max_block_bits` and `extra_max_chunk_bytes` through, and `tableinfo` shows them
- block history (chunkdb 2.0+): `history`, `chunkhistory` and `rangehistory` print one line per event, then `END` or `CURSOR <cursor>`, with `--limit`, `--asc` / `--desc`, `--after` / `--before <cursor>`, `--since` / `--until <ms>` and `--tag <hex>`; `--tag <hex>` on `set`, `unset`, `mset`, `xput`, `xdel`, `chunkset`, `chunksetstate`, `chunkput` and `chunkbatch`; `--at <revision>` or `--at-time <ms>` on `get`, `chunk`, `chunkstate`, `chunkget`, `chunkrange` and `chunkradius`. `tablecreate` and `tableset` pass the history options through, and `tableinfo` shows them. Tags, past reads and listings are refused before sending when the server's HELLO has no `history` capability

### Fixed
- the usage text printed by `help` / `--help` gave `chunksetbin <cx> <cy> <hex>
  | --in <file>`, a form the argument parser rejects: `--in` has to precede the
  coordinates. It now shows both accepted forms, and lists `help` itself among
  the commands
- `chunkget`, `chunkput` and `chunkbatch` took a negative first coordinate for an unknown flag (`chunkget -1 0` failed); negative coordinates now work without `--` in every command
- a coordinate with a leading `+` passed the CLI's check but not the server's; in a `chunkput` header the server then closed the connection. Such coordinates are refused before sending
- after a timeout or a read or write error, the shell kept using the connection, so the next command could read the rest of an earlier reply as its own. The connection is closed instead
- a request line longer than the server's `max_line_bytes` is refused before sending; the server answered `BAD_REQUEST` and closed the connection

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
