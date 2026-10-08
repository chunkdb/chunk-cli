# chunk-cli

`chunk-cli` is the command-line client for [`chunkdb`](https://github.com/chunkdb/chunkdb), a specialized chunk/grid storage engine.

It sends [CQL](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md) statements, one-shot or from an interactive shell, and prints the replies for people or, with `--json`, for scripts.

Speaks `chunkdb` protocol 3; see the engine's [compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).
A server of an earlier protocol is reported as such when connecting.

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

Default URI is `chunk://127.0.0.1:4242/`. A statement is the arguments after the options, joined by spaces; quote it so the shell keeps quotes and `$` as they are.

```bash
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ PING
chunk-cli "CREATE TABLE world (id u16 REQUIRED, light u4 DEFAULT 15, name text(32) NULL) CHUNK 16 x 16"
chunk-cli "SET BLOCK 10 4 IN world id = 7, name = 'door'"
chunk-cli "GET BLOCK 10 4 FROM world"
chunk-cli --blocks "GET CHUNK 0 0 FROM world"
chunk-cli "DESCRIBE world"
```

`SET BLOCK`, `DELETE BLOCK` and `SET CHUNK` print the chunk version after the write; `IF VERSION <v>` writes only while the chunk still has that version, otherwise the statement fails with `VERSION_MISMATCH current=<v>`.

## Output

Values print as the CQL literals that write them: numbers, `true`/`false`, `NULL`, text as `'it''s'`, bytes as `x'0d0a'`, bits as `b'10110'` (the first digit is the lowest bit).

```text
$ chunk-cli "GET BLOCK 10 4 FROM world"
id = 7
light = 15
name = 'door'
$ chunk-cli "GET BLOCK 99 99 FROM world"
NULL
$ chunk-cli --blocks "GET CHUNK 0 0 FROM world COLUMNS id"
version = 2
schema_version = 1
present = 1 of 256 blocks
block 10 4
  id = 7
$ chunk-cli "SHOW TABLES"
1) default
2) world
$ chunk-cli "SCAN CHUNKS FROM world"
chunks:
  1) [0, 0]
more = false
```

- `GET BLOCK` prints one `column = value` line per column, or `NULL` for an absent block.
- `GET CHUNK` prints the chunk version, the schema version (as `DESCRIBE` reports it) and how many blocks are present; with `--blocks` also every present block, by its world coordinates, with its values. `GET AREA` prints the same per chunk under `chunk <cx> <cy>`.
- `DESCRIBE` prints the columns as `CREATE TABLE` writes them, then the options.
- Maps print as `key = value` lines, arrays as numbered rows, an empty array as `(empty)`.
- Chunks are decoded with the table's columns, which the CLI reads with `DESCRIBE` in the same round trip.

With `--json` each reply is one line of JSON: a block is an object of its columns (`null` when absent), integers are exact numbers, `inf`, `-inf` and `nan` are strings, bytes are hex strings, bits strings of `0` and `1`.

```text
$ chunk-cli --json "GET BLOCK 10 4 FROM world"
{"id":7,"light":15,"name":"door"}
$ chunk-cli --json --blocks "GET CHUNK 0 0 FROM world COLUMNS id"
{"version":2,"schema_version":1,"present":1,"block_count":256,"blocks":[{"x":10,"y":4,"values":{"id":7}}]}
```

Errors print as `error: <CODE> <message>` on stderr and the exit status is 1.

## Copying Chunks

`--out <file>` writes a reply's bytes to a file; for `GET CHUNK` that is the chunk form, every column included. `--in <file>` sends a file as the parameter `$1`, which `SET CHUNK` takes:

```bash
chunk-cli --out chunk.bin "GET CHUNK 0 0 FROM world"
chunk-cli --in chunk.bin 'SET CHUNK 4 4 IN world $1'
```

The file is sent as it is. It carries the schema version of the table when it was dumped: if the table's columns changed since (`ALTER TABLE`), `SET CHUNK` fails with `SCHEMA_MISMATCH current=<v>` and nothing changes; dump the chunk again.

## Interactive Shell

```bash
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ shell
```

The shell reads one statement per line with the prompt `chunk>`; a line may start with `--json`, `--blocks`, `--in <file>` or `--out <file>`. `exit` or `quit` leaves the shell, `help` lists this. A failed statement prints its error and the shell goes on.

```text
chunk> PING
PONG
chunk> SET BLOCK 0 0 IN world id = 1
2
chunk> --json GET BLOCK 0 0 FROM world COLUMNS id
{"id":1}
chunk> exit
```

## Usage

```bash
chunk-cli [options] <CQL statement>
chunk-cli [options] shell
chunk-cli version | help
```

Options:

- `--uri <chunk://token@host:port/ | chunks://token@host:port/>`
- `--token <token>`: preferred over the token in the URI
- `--timeout <duration>` (default: `5s`)
- `--tls-insecure` (for self-signed TLS in `chunks://` mode)
- `--tls-server-name <name>`
- `--json`: print replies as JSON
- `--blocks`: print the values of every present block of a chunk
- `--in <file>`: send the file as the parameter `$1`
- `--out <file>`: write the reply's bytes to the file

Statements are single lines. Values are written as literals in the statement; a statement with `$` parameters needs `--in`. Statements name their table, so the URI path is not used.

## TLS (`chunks://`) Example

```bash
chunk-cli --uri chunks://mytoken@127.0.0.1:4242/ --tls-insecure PING
```
