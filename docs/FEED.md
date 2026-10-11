# Watch changes with a durable slot

A watch streams committed block changes.
A durable slot retains changes until your consumer acknowledges them.
A revision orders commits within a table, and an epoch identifies that table's lifetime.
Use the connection variables from the [README](../README.md) and unused `world` and `consumer` names.

1. Create the table, write a block and create a slot.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8) CHUNK 2 x 2"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 1"
chunk-cli --uri "$CHUNKDB_URI" "CREATE SLOT 'consumer' ON world"
```

```output
OK
2
OK
```

2. Start watching and wait for the `start` line before writing in another terminal.

```sh docs-check-watch
chunk-cli --uri "$CHUNKDB_URI" watch world --slot consumer --ack-every 10 --area 0,0,0,0
```

```output
start <epoch>:2
schema <epoch>:3 version 1
change revision 3 time_ms <time-ms> user admin
  block 1 1: {kind = 1} -> {kind = 6}
```

3. In the other terminal, write the change shown above.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 6"
```

```output
3
```

After the complete change prints, press Ctrl-C.
The CLI flushes its completed partial acknowledgement batch and sends `UNWATCH`.
`<epoch>` and `<time-ms>` replace generated identities and timestamps in checked output.

4. Inspect the acknowledgement and remove the example slot.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "SHOW SLOTS ON world"
chunk-cli --uri "$CHUNKDB_URI" "DROP SLOT 'consumer' ON world"
```

```output
1) table = world, name = consumer, epoch = <epoch>, acked = 3, retained_bytes = 0, lost = false
OK
```

## Options

- `--area` uses inclusive chunk coordinates.
- Without `--slot` or `--after`, watching begins after completed writes.
- `--after <epoch:revision>` resumes from a retained position.
- `--slot` resumes from its saved acknowledgement and acknowledges only completely printed changes.
- `--ack-every` requires a slot; its default is 1, and schema events do not count.
- `--json` prints one JSON object per line, including before/after rows and event metadata.

Slot names match `[a-z_][a-z0-9_]*` and contain 1–63 bytes.
Creating and dropping slots requires `ADMIN`.
Watching requires `READ`.

## If it fails

- **resync:** rebuild state using [Resume a consumer](https://github.com/chunkdb/chunkdb/blob/main/docs/FEED_RESUME.md).
- **SLOT_LOST:** rebuild consumer state and replace the slot.
- **Connection lost:** the CLI exits nonzero; reconnect explicitly.
- **Blocked output:** cancellation sends `UNWATCH`, but exit waits for the output write to return.

A crash can repeat printed changes whose acknowledgements are not durable yet.
Changes finishing output after cancellation starts are not acknowledged.
Stdout does not confirm downstream storage.
For exactly-once processing, use a client that stores data and position together before acknowledging.
See [Change feed](https://github.com/chunkdb/chunkdb/blob/main/docs/CHANGE_FEED.md) for recovery rules.
