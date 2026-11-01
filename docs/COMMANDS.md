# Commands

Run a CQL statement, open a shell, or use a CLI command.
Global options precede the command or quoted statement.

```text
chunk-cli [options] <CQL statement>
chunk-cli [options] shell
chunk-cli [options] transaction <file>
chunk-cli [options] watch <table> [--slot name] [--ack-every n] [--area cx0,cy0,cx1,cy1] [--after epoch:revision] [--json]
chunk-cli [options] migrate <file>
chunk-cli [options] migrations
chunk-cli version
chunk-cli help
```

## Options

| Option | Meaning |
| --- | --- |
| `--uri <URI>` | Connection URI; default `chunk://127.0.0.1:4242/` |
| `--user <name>` | Override the URI's login user |
| `--password-file <file>` | Read the login password from the first line |
| `--timeout <duration>` | Network timeout; default `5s` |
| `--no-retry` | Run a transaction file once |
| `--tls-insecure` | Accept an untrusted TLS certificate |
| `--tls-server-name <name>` | Override the TLS certificate name |
| `--hello-option <key=value>` | Supply a HELLO session option; repeat for multiple keys |
| `--json` | Print JSON replies |
| `--blocks` | Include present blocks in chunk, area and scan data output |
| `--in <file>` | Supply binary parameter `$1` |
| `--out <file>` | Write reply bytes to a file |
| `--new-password-file <file>` | Read the new password for CREATE/ALTER USER PASSWORD |

The URI path does not select a table.
The CLI requires `--uri` on each invocation.
`CHUNKDB_URI` in these examples is a shell variable.
See [Connection](CONNECTION.md) for password precedence.

The shell accepts one statement per line, plus `exit`, `quit`, and `help`.
Per-line options are `--json`, `--blocks`, `--in`, `--out` and `--new-password-file`.

## Returns

A block is one cell with named values.
A chunk groups blocks under a version, which changes when its stored data changes.
A schema version identifies the columns used to encode the chunk.
A revision orders committed changes within a table.
Within one table epoch, committed chunk versions are revisions and can be compared numerically.
`DESCRIBE` includes the epoch, completed revision frontier, dimensions, block columns and chunk columns.
Its revision is `NULL` when a read-only server cannot establish the writer's frontier.

- Blocks print `column = value` lines; an absent block prints `NULL`.
- Never-written chunks print `(null)`, or JSON `null`.
- Written empty chunks retain their versioned form while their stored or cached state remains.
- Chunk replies show version, schema version, commit time, chunk columns and presence count; `--blocks` adds rows.
- Area replies group blocks by chunk.
- GET AREA with LIMIT or AFTER prints `more` and `after` with its chunk summaries; JSON preserves the `chunks`, `more`, `after` page map.
- Writes print the affected chunk's new version.

Values print as CQL literals: numbers, booleans, `NULL`, quoted text, `x'hex'` bytes and `b'bits'`.
Bits print lowest bit first.
JSON uses objects for blocks, hex strings for bytes and strings for bits.

Integers print exactly.
JSON integers within inclusive ±9007199254740991 are numbers; larger magnitudes are decimal strings.
This applies to values, coordinates, versions, revisions, timestamps, transaction IDs and numeric error metadata.
Non-finite floats print as strings.
Unknown column types retain their type names and print raw bytes as hex for that column.
Unknown chunk sections are listed by section ID; `--out` preserves their complete bytes.
Chunk summaries validate the complete form without expanding block values.
Area output admits at most 64 MiB of decoded array storage across its chunks before printing anything.
Use a smaller LIMIT or fewer COLUMNS when that bound is exceeded; summaries avoid block arrays, and GET CHUNK with `--out` preserves binary data.
`--blocks` accepts at most 524288 present block-column values per chunk; select fewer columns or use `--out` for a larger chunk.
World coordinates outside signed 64-bit range print as `[chunk,offset]` pairs, including in JSON.

Tables have two or three coordinate axes; a three-axis table requires the third coordinate even when it is zero.
Chunk and area output retains that third coordinate, with x varying fastest within a chunk.
Transaction-private chunk forms print `NULL` for version and commit time and include `private = true`.
Block-column projections retain all chunk columns and include `projected = true` when the form is projected.

`SCAN CHUNKS ... WITH DATA` prints each coordinate, retained version and decoded chunk.
JSON entries contain `{cx,cy,cz?,version,deleted,chunk}`; a tombstone has `deleted: true` and `chunk: null`.
`SINCE` includes retained deletions and skips versions at or below the supplied revision.
The page retains its table epoch, nullable completed frontier and retained deletion floor.
If the supplied revision precedes that floor, start a fresh snapshot before resuming changes.

`GET BLOCK ... WITH VERSION` adds a nullable chunk version beside the values.
The version is `NULL` for never-written chunks, collected empty chunks and transaction-private modified chunks.
An absent block can still have a retained committed chunk version.
`WITH SCHEMA` adds the schema version to a block or area read.
Both clauses together produce version, schema version and values, regardless of clause order.
JSON uses `{version,values}`, `{schema_version,values}` or `{version,schema_version,values}` for blocks and `{schema_version,entries}` for areas.
The CLI refuses to decode rows when returned schema metadata differs from its fetched schema.

## Errors

| Error | When | What to do |
| --- | --- | --- |
| `VERSION_MISMATCH` | A conditional write has an outdated version | Read the chunk again before retrying |
| `SCHEMA_MISMATCH` | An imported chunk has an outdated schema | Export the chunk again |
| NULL binary export | `--out` receives a NULL reply | Write data before exporting |
| `AUTH_REQUIRED` | No login is supplied | Follow [Connection](CONNECTION.md) |

Failed commands print `error: <CODE> <keys> <message>` to stderr.
With `--json`, stderr contains `{error:{code,class,keys,message}}`; local failures use `CLIENT_ERROR` and class `fatal`.
Unknown error codes are fatal; unknown keys remain diagnostic strings.
Classification depends only on the error code, never message text or keys.

| Exit | Class | Codes |
| --- | --- | --- |
| 1 | `fatal` | All other codes, including `SLOT_BUSY`, `MIGRATION_MISMATCH`, `CANCELLED`, `TIMEOUT`, `KILLED` and unknown codes |
| 2 | `reread` | `VERSION_MISMATCH`, `CONDITION_FAILED`, `SCHEMA_MISMATCH` |
| 3 | `rerun` | `CONFLICT`; roll back and rerun the transaction |
| 4 | `backoff` | `BUSY`, `DISK_FULL`, `RATE_LIMITED` |
| 5 | `unknown_outcome` | `OUTCOME_UNKNOWN`; determine whether the write applied before retrying |

[Transaction files](TRANSACTIONS.md#transaction-files) retry known retryable errors; one-shot and interactive statements are not retried.
Watches reconnect unless `--no-reconnect` is supplied; see [Connection](CONNECTION.md#watch-reconnection).
The interactive shell prints statement errors and continues; its exit status describes the session, rather than each statement.
A refused NULL export leaves an existing output file unchanged.

## Examples

Use the connection variables from the [README](../README.md) and an unused `world` table name.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8, name text(16) NULL) CHUNK 2 x 2"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 2, name = 'door'"
chunk-cli --uri "$CHUNKDB_URI" --json "GET BLOCK 1 1 FROM world"
chunk-cli --uri "$CHUNKDB_URI" --blocks "GET CHUNK 0 0 FROM world"
chunk-cli --uri "$CHUNKDB_URI" "GET CHUNK 8 8 FROM world"
```

```output
OK
1
{"kind":2,"name":"door"}
version = 1
schema_version = 1
commit_time_ms = <time-ms>
present = 1 of 4 blocks
block 1 1
  kind = 2
  name = 'door'
(null)
```

Read values with their version and schema metadata:

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" --json "GET BLOCK 1 1 FROM world WITH VERSION WITH SCHEMA"
chunk-cli --uri "$CHUNKDB_URI" --json "GET BLOCK 16 16 FROM world WITH VERSION WITH SCHEMA"
```

```output
{"version":1,"schema_version":1,"values":{"kind":2,"name":"door"}}
{"version":null,"schema_version":1,"values":null}
```

Use a version from a non-NULL chunk reply for a conditional write.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "GET CHUNK 0 0 FROM world" > chunk.txt
read -r label separator version < chunk.txt
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 3 IF VERSION $version"
chunk-cli --uri "$CHUNKDB_URI" --out chunk.bin "GET CHUNK 0 0 FROM world"
chunk-cli --uri "$CHUNKDB_URI" --in chunk.bin 'SET CHUNK 1 0 IN world $1'
chunk-cli --uri "$CHUNKDB_URI" "GET BLOCK 3 1 FROM world"
```

```output
2
wrote 133 bytes to chunk.bin
3
kind = 3
name = 'door'
```

Create a three-axis table with a separate chunk-column row:

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE space (kind u8) CHUNK 2 x 2 x 2 CHUNK COLUMNS (owner text(8) NULL)"
printf 'base' > owner.bin
chunk-cli --uri "$CHUNKDB_URI" --in owner.bin 'SET CHUNK 0 0 0 IN space META owner=$1'
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 0 1 IN space kind = 7"
chunk-cli --uri "$CHUNKDB_URI" "GET BLOCK 1 0 1 FROM space"
```

```output
OK
1
2
kind = 7
```

Use `--blocks "GET CHUNK 0 0 0 FROM space"` to include the present block and chunk columns, or `"SCAN CHUNKS FROM space SINCE 0 WITH DATA"` for retained versions and tombstones.

## Notes

`--out` exports the raw chunk form, including columns whose types this client does not know.
Typed output and binary imports require checked CKCF forms in both table dimensions.
Binary `SET CHUNK --in` imports require known column types and a form within the fetched schema size bound.
The size bound uses the combined hard variable-byte ceiling; the server enforces its current quota while allowing non-growing replacements of older data above a lowered quota.
Supported `text(max)` and `bytes(max)` columns are variable-length known types.
An unknown type is refused before sending the write, with its column name and type in the error.
After a schema change, old chunk files may fail with `SCHEMA_MISMATCH`.
Full chunk imports replace block data and chunk columns.
`SET CHUNK ... COLUMNS ... $1` preserves chunk columns and replaces only the selected block columns.
`SET CHUNK ... META name=$1` updates chunk columns using scalar parameters rather than a chunk-form file.
`IF ABSENT` admits only an absent chunk identity; it cannot be combined with `IF VERSION`.
For statement options, see [CQL reference](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md).
For connection state, see [Transactions](TRANSACTIONS.md).

`SHOW STATUS` reports enabled directory/table features, raw unsigned `compat`, `ro_compat` and `incompat` masks, and `activation_pending`.
`ENABLE FEATURE <name>` explicitly enables a registered feature for the directory and its affected tables.
Both operations require `ADMIN` on `*`; feature activation can change which server versions can open the data.

## Area pages

LIMIT or AFTER selects a GET AREA page and requires the server's `area_paging` capability.
A page bounds visited cells, including empty cells, so an empty chunk list can still have a continuation.
Continue the same selector strictly AFTER the returned coordinate while `more` is true.
The final page has `more = false` and `after = NULL`.
Use exactly two axes for a two-dimensional table and three for a three-dimensional table, including a third zero axis.

```sh
chunk-cli --uri "$CHUNKDB_URI" "GET AREA 0 0 TO 10000 0 FROM world LIMIT 256"
```

The server can stop a page early to honor its configured response-byte limit.
Successful area replies are bounded by the negotiated encoded-byte and chunk-count limits while they are read, including framing, schema and cursor fields.
An oversized, truncated or malformed area reply closes the connection without printing a partial result.
Local decoded-array limits and schema mismatches preserve the connection for a narrower request or an explicit schema refresh.
Use the returned cursor rather than calculating it from the requested LIMIT or the last populated chunk.
Repeat the statement with AFTER followed by the displayed coordinates.
WITH SCHEMA adds a leading schema line; JSON returns `{schema_version, page}` with the complete page map.
Independent requests can observe intervening writes; pages in an open transaction use its snapshot and pending writes.
The CLI runs one page per statement and does not accumulate or retry earlier pages.
