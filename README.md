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

Default URI is `chunk://127.0.0.1:4242/`. A statement is the arguments after the options, joined by spaces; quote it so the shell keeps quotes and `$` as they are. The examples after the first leave out the login (see [Logging In](#logging-in)).

```bash
export CHUNKDB_PASSWORD='change-me'
chunk-cli --uri chunk://admin@127.0.0.1:4242/ PING
chunk-cli "CREATE TABLE world (id u16 REQUIRED, light u4 DEFAULT 15, name text(32) NULL) CHUNK 16 x 16"
chunk-cli "SET BLOCK 10 4 IN world id = 7, name = 'door'"
chunk-cli "GET BLOCK 10 4 FROM world"
chunk-cli --blocks "GET CHUNK 0 0 FROM world"
chunk-cli "DESCRIBE world"
```

`SET BLOCK`, `DELETE BLOCK` and `SET CHUNK` print the chunk version after the write; `IF VERSION <v>` writes only while the chunk still has that version, otherwise the statement fails with `VERSION_MISMATCH current=<v>`.

## Logging In

The CLI logs in as the user of the URI (`chunk://user@host:4242/`) or `--user`. The password comes from the first of:

- `--password-file <file>`: the file's first line;
- the URI, `chunk://user:password@host:4242/`, with `%XX` escapes for `:`, `@`, `/` or `%`;
- the environment variable `CHUNKDB_PASSWORD`;
- a prompt on the terminal (`Password for admin:`), without echo.

Without a user the CLI logs in without one, which only a server started with `--auth none` accepts.

```bash
chunk-cli --user admin --password-file ./admin.password "SHOW TABLES"
```

The password never crosses the network (SCRAM-SHA-256), and the server must prove that it knows the user's password; if it cannot, the connection fails.

```text
$ chunk-cli --uri chunk://admin:wrong@127.0.0.1:4242/ PING
error: connecting failed: AUTH_FAILED invalid user or password
$ chunk-cli PING
error: connecting failed: AUTH_REQUIRED use HELLO 3 USER <name> $1 with a SCRAM-SHA-256 client-first message (log in with --user or chunk://user:password@host/)
```

## Users

Users and rights are CQL statements ([USERS.md](https://github.com/chunkdb/chunkdb/blob/main/docs/USERS.md)). For `CREATE USER` and `ALTER USER ... VERIFIER` write `PASSWORD` instead of `VERIFIER $1`: the CLI reads the new password from `--new-password-file <file>`, or asks for it twice on the terminal, and sends the verifier it computes from it.

```bash
chunk-cli --new-password-file ./bot.password "CREATE USER bot PASSWORD"
chunk-cli "CREATE USER ops PASSWORD MANAGES USERS"
chunk-cli "GRANT READ ON world TO bot"
chunk-cli --uri chunk://bot@127.0.0.1:4242/ "ALTER USER bot PASSWORD"
chunk-cli "REVOKE READ ON world FROM bot"
chunk-cli "DROP USER bot"
```

```text
$ chunk-cli "SHOW USERS"
1)
  name = admin
  manages_users = true
  grants:
    * = ADMIN
2)
  name = bot
  manages_users = false
  grants:
    world = READ
$ chunk-cli --uri chunk://bot@127.0.0.1:4242/ "SET BLOCK 0 0 IN world id = 1"
error: PERMISSION_DENIED WRITE on world
```

A user can change their own password; everything else on users needs `MANAGES USERS`. A table the user has no right on reads as `NO_TABLE`.

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
chunk-cli --uri chunk://admin@127.0.0.1:4242/ shell
```

The shell reads one statement per line with the prompt `chunk>`; a line may start with `--json`, `--blocks`, `--in <file>`, `--out <file>` or `--new-password-file <file>`. `exit` or `quit` leaves the shell, `help` lists this. A failed statement prints its error and the shell goes on.

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

- `--uri <chunk://user:password@host:port/ | chunks://user:password@host:port/>`
- `--user <name>`: the user to log in as, preferred over the URI's
- `--password-file <file>`: the login password, from the file's first line
- `--timeout <duration>` (default: `5s`)
- `--tls-insecure` (for self-signed TLS in `chunks://` mode)
- `--tls-server-name <name>`
- `--json`: print replies as JSON
- `--blocks`: print the values of every present block of a chunk
- `--in <file>`: send the file as the parameter `$1`
- `--out <file>`: write the reply's bytes to the file
- `--new-password-file <file>`: the password of `CREATE USER <name> PASSWORD` and `ALTER USER <name> PASSWORD`

Environment: `CHUNKDB_PASSWORD`, the login password.

Statements are single lines. Values are written as literals in the statement; a statement with `$` parameters needs `--in`. Statements name their table, so the URI path is not used.

## TLS (`chunks://`) Example

```bash
chunk-cli --uri chunks://admin@127.0.0.1:4242/ --tls-insecure PING
```
