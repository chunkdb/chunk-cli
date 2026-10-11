# Commands

Run a CQL statement, open a shell, or use a CLI command.
Global options precede the command or quoted statement.

```text
chunk-cli [options] <CQL statement>
chunk-cli [options] shell
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
| `--tls-insecure` | Accept an untrusted TLS certificate |
| `--tls-server-name <name>` | Override the TLS certificate name |
| `--json` | Print JSON replies |
| `--blocks` | Include present blocks in chunk and area output |
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

- Blocks print `column = value` lines; an absent block prints `NULL`.
- Never-written chunks print `(null)`, or JSON `null`.
- Written empty chunks retain their versioned form while their stored or cached state remains.
- Chunk replies show version, schema version and presence count; `--blocks` adds rows.
- Area replies group blocks by chunk.
- Writes print the affected chunk's new version.

Values print as CQL literals: numbers, booleans, `NULL`, quoted text, `x'hex'` bytes and `b'bits'`.
Bits print lowest bit first.
JSON uses objects for blocks, hex strings for bytes and strings for bits.

Integers print exactly.
Non-finite floats print as strings.

## Errors

| Error | When | What to do |
| --- | --- | --- |
| `VERSION_MISMATCH` | A conditional write has an outdated version | Read the chunk again before retrying |
| `SCHEMA_MISMATCH` | An imported chunk has an outdated schema | Export the chunk again |
| NULL binary export | `--out` receives a NULL reply | Write data before exporting |
| `AUTH_REQUIRED` | No login is supplied | Follow [Connection](CONNECTION.md) |

Failed commands exit 1 and print `error: <CODE> <message>` to stderr.
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
2
{"kind":2,"name":"door"}
version = 2
schema_version = 1
present = 1 of 4 blocks
block 1 1
  kind = 2
  name = 'door'
(null)
```

Use a version from a non-NULL chunk reply for a conditional write.
This example requires Python 3.

```sh docs-check-shell
version=$(chunk-cli --uri "$CHUNKDB_URI" --json "GET CHUNK 0 0 FROM world" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 3 IF VERSION $version"
chunk-cli --uri "$CHUNKDB_URI" --out chunk.bin "GET CHUNK 0 0 FROM world"
chunk-cli --uri "$CHUNKDB_URI" --in chunk.bin 'SET CHUNK 1 0 IN world $1'
chunk-cli --uri "$CHUNKDB_URI" "GET BLOCK 3 1 FROM world"
```

```output
4
wrote 37 bytes to chunk.bin
6
kind = 3
name = 'door'
```

## Notes

After a schema change, old chunk files may fail with `SCHEMA_MISMATCH`.
For statement options, see [CQL reference](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md).
For connection state, see [Transactions](TRANSACTIONS.md).
