# chunk-cli

`chunk-cli` is the command-line client for [`chunkdb`](https://github.com/chunkdb/chunkdb), a specialized chunk/grid storage engine.

It provides direct terminal access to the chunk protocol for operational checks, debugging, and scripting.

Speaks `chunkdb` protocol 2, which chunkdb 2.0 servers serve; see the engine's
[compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).
It does not connect to 1.x servers: use `chunk-cli` 1.x with those.

## Features

- connection URIs:
  - `chunk://` (plain TCP)
  - `chunks://` (TLS)
- block commands: `get`, `set`, `unset`, `mset`, `mget`
- chunks as bit text: `chunk`, `chunkstate`, `chunkset`, `chunksetstate`
- chunks as raw bytes: `chunkget`, `chunkput` (hex or file, optional zrle
  transfer and version check)
- world reads: `chunkexists`, `chunkscan`, `chunkrange`, `chunkradius`
- versions and atomic batches: `chunkver`, `chunkput --if`, `chunkbatch`
- `ping`, `info`, `walflush`, `metrics`, `shell`, `version`
- table commands: `tables`, `tableinfo`, `use`, `tablecreate`, `tableset`,
  `tabledrop`
- table selection via `--table` or the URI path (`chunk://token@host:port/terrain`)
- token via URI (`chunk://token@host:port/`) or `--token`
- clear text output and explicit error messages

## Installation

Requirements:

- Go `1.25.6+`

Build from source:

```bash
go build -o chunk-cli ./cmd/chunk-cli
./chunk-cli version
```

Install into `GOBIN`:

```bash
go install ./cmd/chunk-cli
```

## Quick Start

Default URI is `chunk://127.0.0.1:4242/`.

```bash
chunk-cli ping
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ info
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ set 0 0 1111000011110000
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ get 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ unset 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkstate 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkget --state --out chunk.bin 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkput --state --in chunk.bin 1 0
```

Every command opens a connection with `HELLO 2`, which carries the token and
the table. A wrong token, a missing token, or an unknown table fails the
command before it runs.

Block notes:

- `get <x> <y>` prints the bits, or `(unset)` for a block without a value;
  `mget` prints one such line per block
- `set <x> <y> 000...0` is distinct from `unset <x> <y>`

Chunk notes:

- the chunk commands use the selected table's geometry, which the server
  reports when the connection opens or on `use`
- `chunk <cx> <cy>` prints the payload as bit text; an absent chunk prints
  zero bits, so use `chunkexists` or `chunkstate` to tell it from an all-zero
  chunk
- `chunkstate <cx> <cy>` prints `<payload_bits>|<presence_bits>`, the
  per-block presence included
- `chunkset <cx> <cy> <bits>` replaces the chunk and makes every block present;
  `<bits>` must be a full chunk-sized payload. `chunksetstate` writes mixed
  present/absent state. Both print the chunk's version after the write
- `chunkget` / `chunkput` move the raw bytes: the payload, and with `--state`
  the presence bitmap after it. Bit `i` of the chunk is bit `i % 8` of byte
  `i / 8`. `--zrle` transfers compressed (a `chunkput` is sent compressed only
  when that is smaller)
- `chunkput --if <version>` and `chunkbatch --if <version>` apply only while
  the chunk still has that version, as printed by `chunkver` or by the last
  write; otherwise they fail with `VERSION_MISMATCH current=<version>`

## Interactive Shell

Start the interactive shell:

```bash
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ shell
```

The shell prompt is `chunk>`, or `chunk:<table>>` once a table is selected
(`--table`, the URI path, or `use <table>`). The shell accepts every network
command from the reference below, plus:

- `quit`: sends `QUIT` and exits
- `exit`: exits locally

Example session:

```text
chunk> ping
PONG
chunk> get 0 0
(unset)
chunk> set 0 0 1111000011110000
OK
chunk> get 0 0
1111000011110000
chunk> chunkexists 0 0
1
chunk> chunkver 0 0
3
chunk> chunkstate 0 0
<payload_bits>|<presence_bits>
chunk> chunkget --state 0 0
bytes=<n>
<hex dump>
chunk> use terrain
table=terrain
...
chunk:terrain> quit
BYE
```

## Usage

```bash
chunk-cli [global options] <command> [command args]
```

Global options:

- `--uri <chunk://token@host:port/ | chunks://token@host:port/>`
- `--token <token>`: preferred over the token in the URI
- `--table <table>`: the table to work on; default: the URI path
  (`chunk://token@host:port/<table>`), else the server's `default` table
- `--timeout <duration>` (default: `5s`)
- `--tls-insecure` (for self-signed TLS in `chunks://` mode)
- `--tls-server-name <name>`

## Command Reference

- `ping`
  - sends `PING`, expects simple response (`+PONG`)
- `info`
  - sends `INFO`, prints the selected table's runtime statistics
- `get <x> <y>`
  - sends `GET`, prints the block's bits or `(unset)`
- `set <x> <y> <bits>`
  - sends `SET`; validates `bits` as binary (`0`/`1`) before request
- `unset <x> <y>`
  - sends `UNSET`, clears explicit block presence, prints simple response
- `mset <x> <y> <bits> [<x> <y> <bits> ...]`
  - sends `MSET` (one round-trip for many blocks); validates each `bits`; prints simple response
  - items apply in order and are not atomic as a group: on a server error, earlier items may already be applied (use `chunkbatch` for an atomic single-chunk update)
- `mget <x> <y> [<x> <y> ...]`
  - sends `MGET` (one round-trip for many blocks); prints one line per block:
    its bits or `(unset)`
- `chunkexists <cx> <cy>`
  - sends `CHUNKEXISTS`, prints `1` when the chunk has explicit presence and `0` when absent
- `chunk <cx> <cy>`
  - sends `CHUNKGET`, prints the payload as bit text
- `chunkstate <cx> <cy>`
  - sends `CHUNKGET ... STATE`; prints `<payload_bits>|<presence_bits>`
- `chunkset <cx> <cy> <bits>`
  - sends `CHUNKPUT` with the payload; checks the bit count against the
    table's geometry before sending; prints the chunk's version
- `chunksetstate <cx> <cy> <payload_bits>|<presence_bits>`
  - sends `CHUNKPUT ... STATE`; same checks and output as `chunkset`
- `chunkget [--state] [--zrle] [--out <file>] <cx> <cy>`
  - sends `CHUNKGET`
  - default output: byte count (and with `--zrle` the compressed size) + hex dump
  - with `--out`: writes the bytes to file and prints a summary
- `chunkput [--state] [--zrle] [--if <version>] <cx> <cy> <hex>` |
  `chunkput [flags] --in <file> <cx> <cy>`
  - sends `CHUNKPUT` with the bytes `chunkget` prints, given as hex after the
    coordinates or, with `--in`, read from a file; flags come before the
    coordinates. Checks the size against the table's geometry before sending;
    prints the chunk's version
- `chunkscan <limit> [<cursor_cx> <cursor_cy>]`
  - sends `CHUNKSCAN`; prints one line per array item: first `END` or
    `CURSOR <cx> <cy>` (pass those coordinates to the next call to continue),
    then one `<cx> <cy>` line per populated chunk
- `chunkrange <cx0> <cy0> <cx1> <cy1>`
  - sends `CHUNKRANGE` (max 256 chunks); prints one
    `<cx> <cy> <payload_bits>|<presence_bits>` line per populated chunk
- `chunkradius <cx> <cy> <radius_chunks>`
  - sends `CHUNKRADIUS` (populated chunks within a disc of `radius_chunks`
    around `<cx> <cy>`, max 256 chunks); same output shape as `chunkrange`
- `chunkver <cx> <cy>`
  - sends `CHUNKVER`; prints the chunk's opaque version token
- `chunkbatch [--if <version>] <cx> <cy> SET <x> <y> <bits> | UNSET <x> <y> ...`
  - sends `CHUNKBATCH` (atomic within one chunk); prints the chunk's version
    after the batch
- `walflush`
  - sends `WALFLUSH`; on `OK`, all previously acknowledged writes are durable
    even when the server runs in `relaxed` durability mode
- `metrics`
  - sends `METRICS`; prints Prometheus text-format runtime metrics
- `tables`
  - sends `TABLES`; prints one table name per line
- `tableinfo <table>`
  - sends `TABLEINFO`; prints the table's `key=value` lines (geometry,
    options, store id)
- `use <table>`
  - sends `USE`; prints the same lines as `tableinfo`. In the shell, later
    commands work on that table; as a single command it only checks the
    table, so use `--table` to run a command on a table
- `tablecreate <table> block_bits <n> [<key> <value> ...]`
  - sends `TABLECREATE`; keys are the `tableinfo` names, for example
    `chunk_width_blocks 32 durability_mode fsync-wal`
- `tableset <table> <option> <value> [<option> <value> ...]`
  - sends `TABLESET`; options are `durability_mode`, `checkpoint_updates`,
    `checkpoint_wal_bytes`, `wal_group_commit_updates`, `checkpoint_compression`
- `tabledrop <table>`
  - sends `TABLEDROP`; deletes the table and its data
- `shell`
  - starts interactive mode with prompt `chunk>` (or `chunk:<table>>`)
- `version`
  - prints the CLI version and exits; opens no connection
- `help` (also `--help`, `-h`)
  - prints usage, commands, and global options; opens no connection

## TLS (`chunks://`) Example

```bash
chunk-cli --uri chunks://mytoken@127.0.0.1:4242/ --tls-insecure info
```

## Output and Errors

- normal responses are printed in readable form (text commands preserve server text; `chunkget` includes the byte count)
- errors are printed as `error: ...` and process exits non-zero
- server `-ERR ...` responses are surfaced directly
- a server without protocol 2 (chunkdb 1.x) is reported as such when connecting
