# Write several blocks together

Use the shell to publish several changes in one transaction.
A transaction reads a snapshot and publishes its writes together on one connection and one table.
Use the connection variables from the [README](../README.md) and an unused `world` name.

1. Create a table.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8) CHUNK 2 x 2"
```

```output
OK
```

2. Begin, write two blocks, and commit on the shell's connection.

```sh
chunk-cli --uri "$CHUNKDB_URI" shell
```

```cql docs-check
BEGIN
SET BLOCK 0 0 IN world kind = 4
SET BLOCK 1 1 IN world kind = 5
COMMIT
GET BLOCK 1 1 FROM world
```

```output
OK
(applies at COMMIT)
(applies at COMMIT)
1
kind = 5
```

The `chunk*>` prompt means a transaction is open.
Writes print `(applies at COMMIT)` until `COMMIT` publishes them.
A revision orders committed changes within the table; committed chunks share that revision.
A commit without writes prints `(nothing written)`.

## If it fails

| Error | When | What to do |
| --- | --- | --- |
| `CONFLICT` | Another write changes a chunk you read | Send `ROLLBACK`, then retry from `BEGIN` |
| One-shot transaction refused | You send `BEGIN`, `COMMIT` or `ROLLBACK` outside the shell | Use `shell` |
| Statement refused | You use an unsupported statement inside a transaction | Roll back before running it |

After `CONFLICT`, the transaction writes nothing and keeps returning the conflict until `ROLLBACK`.
Interactive shell transactions are retried manually.
Transaction files use the bounded retries described below.
An ordinary statement error prints to stderr, and the shell continues.

`ROLLBACK`, leaving the shell or losing the connection discards uncommitted writes.
Transactions allow GET, SET, DELETE, DESCRIBE and PING.
Conditional `IF VERSION` writes and other statements are refused inside a transaction.
See [Transactions](https://github.com/chunkdb/chunkdb/blob/main/docs/TRANSACTIONS.md) for snapshot and conflict rules.

## Transaction files

Put one CQL statement per line in a file, then run it as one transaction.
Blank lines and lines starting with `--` are ignored.
An outer `BEGIN` and `COMMIT` pair is optional; nested transaction controls are refused.

```sh docs-check-shell
cat > changes.cql <<'CQL'
SET BLOCK 0 0 IN world kind = 6
SET BLOCK 1 1 IN world kind = 7
CQL
chunk-cli --uri "$CHUNKDB_URI" transaction changes.cql
```

```output
OK
(applies at COMMIT)
(applies at COMMIT)
2
```

`chunk-cli --uri "$CHUNKDB_URI" --in changes.cql transaction` reads the same file.
Use `transaction -` or `--in - transaction` to read it from standard input.
`--in` selects the statement file for this command; binary parameter files and per-line CLI options are not supported inside it.
`--json` applies to every reply.
Replies are printed only after a successful commit, so failed attempts produce no output.

The entire file is read before connecting and runs again from `BEGIN` for `rerun`, `reread` and `backoff` error classes.
Each attempt uses a new connection, so an unfinished transaction is discarded before another attempt.
There are at most six attempts, with delays of 25, 50, 100, 200 and 400 milliseconds.
`--no-retry` runs the file once.
The final error preserves its code and exit class and reports the number of attempts.
A connection failure or `OUTCOME_UNKNOWN` is reported without replay because the commit may have applied.
