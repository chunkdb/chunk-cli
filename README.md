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

Install the 2.0 CLI into `GOBIN`:

```bash
go install github.com/chunkdb/chunk-cli/v2/cmd/chunk-cli@v2.0.0
```

From a local checkout, use `go install ./cmd/chunk-cli`.

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

`chunk-cli "BACKUP TO 'snapshot'"` creates a backup on the server's filesystem under its configured `--backup-dir` and prints table cuts and counts.
It requires `MANAGES USERS`; see the server's [backup guide](https://github.com/chunkdb/chunkdb/blob/main/docs/BACKUP.md).

## Named migrations

Run this at every start of your app, before issuing its data statements:

```bash
chunk-cli --uri chunk://admin@127.0.0.1:4242/ migrate migrations.cql
chunk-cli --uri chunk://admin@127.0.0.1:4242/ migrations
```

Lowercase `migrate <file>` selects the file command.
To send a CQL statement, quote the whole statement, for example
`chunk-cli "MIGRATE 'create_world' CREATE TABLE world (id u8) CHUNK 2 x 2"`.
Other keyword cases and a lowercase `migrate` followed by a quoted name are
also sent as statements.

`migrations.cql` contains named steps, each followed by one schema statement:

```sql
-- migrate: create_world
CREATE TABLE world (id u16, name text(32) NULL) CHUNK 16 x 16
-- migrate: add_light
ALTER TABLE world ADD COLUMN light u4 DEFAULT 15
```

The command prints `create_world applied`, then `add_light applied`.
Running the same file again prints `skipped` for each step, including when two
applications start concurrently. Keep an applied name and its statement text
unchanged; add a new step for the next schema change. A changed statement under
the same name fails with `CONFLICT`. The command stops at the first error,
prints the step name and server error code on stderr, and exits with status 1.
Earlier successful steps remain applied.

Names match `[a-z_][a-z0-9_]*`, 1–63 bytes. Blank lines and full-line `--`
comments are ignored. A comment starting with the word `migrate` must use the
exact `-- migrate:` marker spelling and spacing; near markers report their
line number instead of merging steps. Multiline statements are joined with one space after
trimming each line; quoted values must fit on one line. Interior spacing and
case are preserved and form part of the migration identity. Statements need
no semicolons; separators and `$` parameters outside quotes are rejected.
The whole file is checked for these rules before connecting, while the server
validates each statement's CQL syntax and permissions when that step runs.

Supported steps are CREATE/ALTER/DROP TABLE, GRANT/REVOKE and CREATE/DROP SLOT.
Each step requires the same rights as its inner statement; already applied
steps still require those rights. `migrations` lists names, application times,
users and statement text in applied order and requires MANAGES USERS. `--json`
prints one `{name,status}` object per applied/skipped step, or the full history
array for `migrations`. See the server's
[named migration reference](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md#named-migrations)
for recovery and permission details.

## Watching changes

A server supporting WATCH can stream committed changes of a table:

```bash
chunk-cli watch world --area 0,0,3,3
chunk-cli watch world --after a1f8d6716431b4702a5daeeb9cda1c5d:4 --json
```

`--area` bounds are inclusive **chunk** coordinates. `--after` resumes after an
`epoch:revision` position still retained by the server. Without it, the stream
starts after completed writes. Output begins with that start position, followed
by a header and one before/after line per changed block:

```text
start a1f8d6716431b4702a5daeeb9cda1c5d:0
change revision 4 time_ms 1791575377810 user admin
  block 0 0: (absent) -> {id = 18446744073709551615, name = 'door', flags = b'10110', blob = x'00ff'}
```

Values use the same literals as other CLI output; `(absent)` denotes a missing
block, while `NULL` denotes a null column. Anonymous changes print
`user (anonymous)`. Coordinates beyond int64 print as `[chunk,offset]`.
`schema` lines identify schema changes. A `resync` line means the position is no
longer available: keep reading while re-reading state on another connection,
apply subsequent changes only when their revision exceeds each chunk's version,
and retain the reported frontier with the rebuilt state before applying later
changes.

`--json` emits one object per line, including the initial `start`, with `type`
and `position` (`epoch`, `revision`). Change objects include `commit_time_ms`,
`user`, `schema_version` and `blocks` with `x`, `y`, `before` and `after`;
absent rows are JSON null. Values follow the CLI's JSON conventions below.

Ctrl-C sends UNWATCH, drains its acknowledgement and exits successfully. A lost
connection or server error ends the command with a non-zero status; it does not
reconnect automatically. WATCH runs through this command, outside the shell.
Schemas are retained by version for the lifetime of a stream. If replay needs an
older schema unavailable through DESCRIBE or earlier schema events, the command
fails explicitly instead of decoding those rows with the current schema.

### Durable slots

On a server supporting durable slots, create a slot before the changes you need
to keep. `--slot` resumes from its written acknowledgement and sends ACK only
after a complete change has printed. The default is every change;
`--ack-every <n>` batches that many changes and requires `--slot`. Ctrl-C also
acknowledges a partially filled batch before UNWATCH. A change whose output
finishes after Ctrl-C begins is not acknowledged. UNWATCH is sent even while
an event's output is blocked; the command finishes once the pending event output
write returns. Schema descriptions do not count toward the batch. Slot names
match `[a-z_][a-z0-9_]*`, 1–63 bytes.

This example uses a fresh `world` table with one `u8` column. While the watch is
running, another terminal writes `SET BLOCK 0 0 IN world id = 7`:

```text
$ chunk-cli "CREATE TABLE world (id u8) CHUNK 2 x 2"
OK
$ chunk-cli "CREATE SLOT 'consumer' ON world"
OK
$ chunk-cli watch world --slot consumer
start 206a190bc6ce96ffcac34fcfc8322257:0
schema 206a190bc6ce96ffcac34fcfc8322257:2 version 1
change revision 2 time_ms 1791585663562 user admin
  block 0 0: (absent) -> {id = 7}
^C
```

After Ctrl-C, the written position is visible through SHOW SLOTS:

```text
$ chunk-cli "SHOW SLOTS ON world"
1) table = world, name = consumer, epoch = 206a190bc6ce96ffcac34fcfc8322257, acked = 2, retained_bytes = 0, lost = false
$ chunk-cli watch world --slot consumer --ack-every 10 --json
{"type":"start","position":{"epoch":"206a190bc6ce96ffcac34fcfc8322257","revision":2}}
^C
$ chunk-cli "DROP SLOT 'consumer' ON world"
OK
```

CREATE/DROP SLOT require ADMIN on the table; watching requires READ. SHOW SLOTS
also works in the shell. `--after epoch:revision` can resume beyond the slot's
written acknowledgement. A lost slot ends the command with `SLOT_LOST`; rebuild
consumer state, drop the slot and create it again.

A crash can repeat printed changes, including ACKs the server has not persisted.
Printing to stdout does not confirm that a downstream process stored the output.
For exactly-once output, use a client that atomically stores `(epoch, revision)`
with its output, acknowledges after that commit, and reconnects AFTER the stored
position. See the server's [durable slot guide](https://github.com/chunkdb/chunkdb/blob/main/docs/CHANGE_FEED.md#durable-slots).

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

## Transactions

In the shell, `BEGIN` starts a [transaction](https://github.com/chunkdb/chunkdb/blob/main/docs/TRANSACTIONS.md) on the shell's connection and the prompt turns `chunk*>`. Its reads see one snapshot of one table, its writes print `(applies at COMMIT)`, and `COMMIT` applies them together and prints the version every written chunk now has (`(nothing written)` without writes).

```text
chunk> BEGIN
OK
chunk*> GET BLOCK 10 4 FROM world COLUMNS id
id = 7
chunk*> SET BLOCK 10 4 IN world id = 8
(applies at COMMIT)
chunk*> SET BLOCK 300 7 IN world id = 9
(applies at COMMIT)
chunk*> COMMIT
5
chunk>
```

`ROLLBACK` discards the writes; leaving the shell or losing the connection does too. Inside a transaction only `GET`, `SET`, `DELETE`, `DESCRIBE` and `PING` run, without `IF VERSION`; other statements fail with `INVALID_ARGUMENT` and the transaction stays open.

If another write changed a chunk the transaction read or wrote, `COMMIT` (or a statement inside) fails with `CONFLICT` and nothing is written; run the transaction again from `BEGIN`. The CLI does not retry.

```text
chunk*> COMMIT
error: CONFLICT chunk_changed a chunk the transaction read or wrote changed after its snapshot (the transaction ended and wrote nothing; run it again from BEGIN)
chunk>
```

A one-shot statement closes its connection, so `BEGIN`, `COMMIT` and `ROLLBACK` there fail with an error that points to the shell.

## Usage

```bash
chunk-cli [options] <CQL statement>
chunk-cli [options] shell
chunk-cli [options] watch <table> [--slot name [--ack-every n]] [--area cx0,cy0,cx1,cy1] [--after epoch:revision] [--json]
chunk-cli [options] migrate <file>
chunk-cli [options] migrations
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
