# Transactions in the shell

A transaction belongs to one connection and one table; use `shell`, because a one-shot command closes its connection.
This example reads a snapshot and changes two blocks together:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" shell <<'CQL'
BEGIN
GET BLOCK 0 0 FROM world
SET BLOCK 0 0 IN world kind = 4
SET BLOCK 1 1 IN world kind = 5
COMMIT
GET BLOCK 1 1 FROM world
exit
CQL
```

The prompt is `chunk*>` while the transaction is open.
Writes print `(applies at COMMIT)`; COMMIT prints the revision shared by written chunks, or `(nothing written)` for no writes.
ROLLBACK, leaving the shell or losing the connection discards uncommitted writes.
Within a transaction, GET, SET, DELETE, DESCRIBE and PING are allowed; conditional `IF VERSION` and other statements are refused.

CONFLICT ends the transaction without publishing its writes; retry explicitly from BEGIN.
The CLI does not retry transactions.
A failed ordinary statement prints its error and the shell continues.
See the server's [transaction guide](https://github.com/chunkdb/chunkdb/blob/main/docs/TRANSACTIONS.md) for snapshot and conflict rules.

[README](../README.md)
