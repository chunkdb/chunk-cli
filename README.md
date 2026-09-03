# chunk-cli

[![CI](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml)

`chunk-cli` is the command-line client for [`chunkdb`](https://github.com/chunkdb/chunkdb), a specialized chunk/grid storage engine.

It provides direct terminal access to the chunk protocol for operational checks, debugging, and scripting.

Targets the stable `chunkdb` 1.x protocol; see the engine's
[compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

## Features

- connection URIs:
  - `chunk://` (plain TCP)
  - `chunks://` (TLS)
- core commands:
  - `ping`
  - `info`
  - `auth`
  - `get`
  - `exists`
  - `set`
  - `unset`
  - `mset`
  - `mget`
  - `chunkexists`
  - `chunkset`
  - `chunkstate`
  - `chunksetstate`
  - `chunksetbin`
  - `chunksetbinstate`
  - `chunk`
  - `chunkbin`
  - `chunkbinstate`
  - `chunkbinc`
  - `chunkbincstate`
  - `chunkscan`
  - `chunkrange`
  - `chunkradius`
  - `chunkver`
  - `chunkcas`
  - `chunkbatch`
  - `walflush`
  - `metrics`
  - `shell`
- token auth via URI (`chunk://token@host:port/`) or `--token`
- clear text output and explicit error messages

## Installation

Requirements:

- Go `1.25+`

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
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ get 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ exists 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ set 0 0 10110011
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ unset 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkexists 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkset 0 0 <full_chunk_bits>
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkstate 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunksetstate 0 0 <payload_bits>|<presence_bits>
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunk 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkbin 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkbinstate 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunksetbin 0 0 <hex_payload_bytes>
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunksetbinstate --in state.bin 0 0
```

Block-state note:

- `get <x> <y>` prints zero bits for an unset block
- `exists <x> <y>` prints `1` when the block is explicitly present, `0` when it is unset
- `set <x> <y> 000...0` is distinct from `unset <x> <y>`

Chunk-state note:

- `chunk <cx> <cy>` still prints zero bits for an absent chunk
- `chunkexists <cx> <cy>` prints `1` when any explicit chunk presence exists, `0` when the chunk is unset/absent
- `chunkset <cx> <cy> 000...0` is distinct from an absent chunk, but `<bits>` must be a full chunk-sized payload
- `chunkstate <cx> <cy>` prints `<payload_bits>|<presence_bits>` for exact per-block presence
- `chunksetstate <cx> <cy> <payload_bits>|<presence_bits>` writes mixed present/absent block state
- `chunkbinstate <cx> <cy>` prints exact chunk-state bytes as `[payload_bytes][presence_bytes]`
- `chunkbinc <cx> <cy>` / `chunkbincstate <cx> <cy>` fetch the same bytes as `chunkbin`/`chunkbinstate` over the compressed `CHUNKBINC` transfer and decompress client-side; pass `--raw` to keep the compressed payload
- `chunksetbin <cx> <cy> <hex>` / `chunksetbinstate <cx> <cy> <hex>` write raw chunk bytes in the layouts `chunkbin`/`chunkbinstate` print; `--in <file>` reads the bytes from a file (for example one written by `chunkbin --out`). Requires chunkdb server 1.3+
- `chunkradius <cx> <cy> <radius_chunks>` reads populated chunks within a chunk-space radius (disc), like `chunkrange` but circular

## Interactive Shell

Start the interactive shell:

```bash
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ shell
```

The shell prompt is `chunk>`. Supported shell commands:

- `ping`
- `info`
- `auth [token]`
- `get <x> <y>`
- `exists <x> <y>`
- `set <x> <y> <bits>`
- `unset <x> <y>`
- `mset <x> <y> <bits> [<x> <y> <bits> ...]`
- `mget <x> <y> [<x> <y> ...]`
- `chunkexists <cx> <cy>`
- `chunkset <cx> <cy> <bits>`
- `chunkstate <cx> <cy>`
- `chunksetstate <cx> <cy> <payload_bits>|<presence_bits>`
- `chunksetbin <cx> <cy> <hex> | --in <file>`
- `chunksetbinstate <cx> <cy> <hex> | --in <file>`
- `chunk <cx> <cy>`
- `chunkbin [--out <file>] <cx> <cy>`
- `chunkbinstate [--out <file>] <cx> <cy>`
- `chunkbinc [--out <file>] [--raw] <cx> <cy>`
- `chunkbincstate [--out <file>] [--raw] <cx> <cy>`
- `chunkscan <limit> [<cursor_cx> <cursor_cy>]`
- `chunkrange <cx0> <cy0> <cx1> <cy1>`
- `chunkradius <cx> <cy> <radius_chunks>`
- `chunkver <cx> <cy>`
- `chunkcas <cx> <cy> <version> <payload_bits>|<presence_bits>`
- `chunkbatch <cx> <cy> <version|-> SET <x> <y> <bits> | UNSET <x> <y> ...`
- `walflush`
- `metrics`
- `quit`
- `exit`

Example session:

```text
chunk> ping
PONG
chunk> exists 0 0
0
chunk> set 0 0 1111000011110000
OK
chunk> exists 0 0
1
chunk> get 0 0
1111000011110000
chunk> unset 0 0
OK
chunk> chunkexists 0 0
0
chunk> chunkset 0 0 <full_chunk_bits>
OK
chunk> chunkstate 0 0
<full_chunk_bits>|<presence_bits>
chunk> chunksetstate 0 0 <payload_bits>|<presence_bits>
OK
chunk> chunkexists 0 0
1
chunk> chunk 0 0
<full_chunk_bits>
chunk> chunkbinstate 0 0
bytes=<n>
<hex dump>
chunk> get 0 0
0000000000000000
chunk> info
chunkdb_version=1
...
chunk> quit
BYE
```

Shell auth behavior:

- if token is present in URI or `--token`, shell performs automatic `AUTH` on connect
- you can re-authenticate at any time with `auth <token>`
- `exit` exits locally; `quit` sends `QUIT` and exits

## Usage

```bash
chunk-cli [global options] <command> [command args]
```

Global options:

- `--uri <chunk://token@host:port/ | chunks://token@host:port/>`
- `--token <token>`
- `--timeout <duration>` (default: `5s`)
- `--tls-insecure` (for self-signed TLS in `chunks://` mode)
- `--tls-server-name <name>`

Auth behavior:

- for non-`auth`/non-`shell` commands, CLI auto-runs `AUTH` when token is present in URI or `--token`
- for `auth`, token is taken from `auth <token>` first, otherwise from URI/`--token`
- for `shell`, token is auto-authenticated once on connect (if provided)

## Command Reference

- `ping`
  - sends `PING`, expects simple response (`+PONG`)
- `info`
  - sends `INFO`, prints returned bulk text
- `auth <token>`
  - sends `AUTH <token>`, prints simple response
- `get <x> <y>`
  - sends `GET`, prints block bit payload
- `exists <x> <y>`
  - sends `EXISTS`, prints `1` when present and `0` when unset
- `set <x> <y> <bits>`
  - sends `SET`; validates `bits` as binary (`0`/`1`) before request
- `unset <x> <y>`
  - sends `UNSET`, clears explicit block presence, prints simple response
- `mset <x> <y> <bits> [<x> <y> <bits> ...]`
  - sends `MSET` (one round-trip for many blocks); validates each `bits`; prints simple response
  - items apply in order and are not atomic as a group: on a server error, earlier items may already be applied (use `chunkbatch` for an atomic single-chunk update)
- `mget <x> <y> [<x> <y> ...]`
  - sends `MGET` (one round-trip for many blocks); prints one bit payload per line
- `chunkexists <cx> <cy>`
  - sends `CHUNKEXISTS`, prints `1` when the chunk has explicit presence and `0` when absent
- `chunkset <cx> <cy> <bits>`
  - sends `CHUNKSET`; validates `bits` as binary (`0`/`1`) before request
- `chunkstate <cx> <cy>`
  - sends `CHUNK ... STATE`; prints `<payload_bits>|<presence_bits>`
- `chunksetstate <cx> <cy> <payload_bits>|<presence_bits>`
- `chunksetbin <cx> <cy> <hex> | --in <file>`
- `chunksetbinstate <cx> <cy> <hex> | --in <file>`
  - sends `CHUNKSET ... STATE`; validates both halves as binary before request
- `chunk <cx> <cy>`
  - sends `CHUNK`, prints text chunk payload
- `chunkbin [--out <file>] <cx> <cy>`
  - sends `CHUNKBIN`
  - default output: payload size + hex dump
  - with `--out`: writes raw bytes to file and prints summary
- `chunkbinstate [--out <file>] <cx> <cy>`
  - sends `CHUNKBIN ... STATE`
  - default output: exact chunk-state size + hex dump
  - with `--out`: writes raw state bytes to file and prints summary
- `chunkscan <limit> [<cursor_cx> <cursor_cy>]`
  - sends `CHUNKSCAN`; prints one line per array item: first `END` or
    `CURSOR <cx> <cy>` (pass those coordinates to the next call to continue),
    then one `<cx> <cy>` line per populated chunk
- `chunkrange <cx0> <cy0> <cx1> <cy1>`
  - sends `CHUNKRANGE` (max 256 chunks); prints one
    `<cx> <cy> <payload_bits>|<presence_bits>` line per populated chunk
- `chunkver <cx> <cy>`
  - sends `CHUNKVER`; prints the chunk's opaque version token
- `chunkcas <cx> <cy> <version> <payload_bits>|<presence_bits>`
  - sends `CHUNKCAS ... STATE ...`; prints the new version on success; a
    stale version fails with `VERSION_MISMATCH current=<version>`
- `chunkbatch <cx> <cy> <version|-> SET <x> <y> <bits> | UNSET <x> <y> ...`
  - sends `CHUNKBATCH` (atomic within one chunk); pass `-` to skip the
    version check; prints the new version on success
- `walflush`
  - sends `WALFLUSH`; on `OK`, all previously acknowledged writes are durable
    even when the server runs in `relaxed` durability mode
- `metrics`
  - sends `METRICS`; prints Prometheus text-format runtime metrics
- `shell`
  - starts interactive mode with prompt `chunk>`

## TLS (`chunks://`) Example

```bash
chunk-cli --uri chunks://mytoken@127.0.0.1:4242/ --tls-insecure info
```

## Output and Errors

- normal responses are printed in readable form (text commands preserve server text; `chunkbin` includes byte count)
- errors are printed as `error: ...` and process exits non-zero
- server `-ERR ...` responses are surfaced directly

## Development

Run local checks:

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./...
```

Show help:

```bash
go run ./cmd/chunk-cli --help
```
