# CLI commands for chunkdb 2.0

Global options precede the command or quoted CQL statement.

```text
chunk-cli [options] <CQL statement>
chunk-cli [options] shell
chunk-cli [options] watch <table> [--slot name] [--ack-every n] [--area cx0,cy0,cx1,cy1] [--after epoch:revision] [--json]
chunk-cli [options] migrate <file>
chunk-cli [options] migrations
chunk-cli version
chunk-cli help
```

| Option | Meaning |
| --- | --- |
| `--uri <URI>` | `chunk://` or `chunks://`; default `chunk://127.0.0.1:4242/` |
| `--user <name>` | Override the URI's login user |
| `--password-file <file>` | Login password from the first line |
| `--timeout <duration>` | Network timeout; default `5s` |
| `--tls-insecure` | Accept an untrusted TLS certificate |
| `--tls-server-name <name>` | TLS certificate name override |
| `--json` | Print JSON replies |
| `--blocks` | Include present blocks in chunk/area output |
| `--in <file>` | Supply binary parameter `$1` |
| `--out <file>` | Write reply bytes to a file |
| `--new-password-file <file>` | New password for CREATE/ALTER USER PASSWORD |

`CHUNKDB_PASSWORD` supplies the login password; [connection rules](CONNECTION.md) define precedence.
Statements name their table; the URI path does not select a table.
The shell accepts one statement per line, or `exit`, `quit`, and `help`.
Per-line options are `--json`, `--blocks`, `--in`, `--out` and `--new-password-file`.

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "DESCRIBE world"
./build/chunk-cli --uri "$CHUNKDB_URI" --blocks "GET CHUNK 0 0 FROM world"
./build/chunk-cli --uri "$CHUNKDB_URI" --json "GET BLOCK 1 1 FROM world"
./build/chunk-cli --uri "$CHUNKDB_URI" "SCAN CHUNKS FROM world"
```

Blocks print `column = value` lines; an absent block prints `NULL`.
A never-written chunk prints `(null)`, or JSON `null`. A written chunk with all blocks deleted retains its empty form and version until its disk artifacts and cached state are removed. `--out` refuses a NULL reply and leaves an existing output file unchanged.
Chunk replies show version, schema version and presence count; `--blocks` adds rows, and AREA groups output by chunk.
Values use CQL literals: numbers, booleans, `NULL`, quoted text, `x'hex'` bytes and `b'bits'` (lowest bit first).
JSON blocks are objects; absent rows/null columns are null, bytes are hex strings and bits are strings.
Integers print exactly; non-finite floats print as strings.

SET/DELETE return the chunk's new version.
Use a version returned by a non-NULL GET CHUNK for a conditional write (this shell example uses Python 3 to read the JSON version):

```bash
version=$(./build/chunk-cli --uri "$CHUNKDB_URI" --json "GET CHUNK 0 0 FROM world" | python3 -c 'import json,sys; print(json.load(sys.stdin)["version"])')
./build/chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 0 0 IN world kind = 3 IF VERSION $version"
```

A competing write causes `VERSION_MISMATCH current=<version>`; this statement changes nothing.
Copy all columns as a binary chunk form:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" --out chunk.bin "GET CHUNK 0 0 FROM world"
./build/chunk-cli --uri "$CHUNKDB_URI" --in chunk.bin 'SET CHUNK 1 0 IN world $1'
```

The chunk form records its schema version; after ALTER, an old file may fail with `SCHEMA_MISMATCH` and must be dumped again.
Errors go to stderr as `error: <CODE> <message>` and exit 1.
See [CQL](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md) for statement syntax and [transactions](TRANSACTIONS.md) for shell connection state.
