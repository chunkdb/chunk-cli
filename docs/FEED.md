# Watch changes with a durable slot

A watch streams committed block changes.
A durable slot retains changes until your consumer acknowledges them.
A revision orders commits within a table, and an epoch identifies that table's lifetime.
Use the connection variables from the [README](../README.md) and unused `world` and `consumer` names.

1. Create the table, write a block and create a slot.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8) CHUNK 2 x 2"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 1"
chunk-cli --uri "$CHUNKDB_URI" "CREATE SLOT 'consumer' ON world AREA 0 0 TO 0 0"
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

The stream may include periodic `progress <epoch>:<revision>` lines before or after the illustrated change, including while idle.

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
1)
  table = world
  name = consumer
  epoch = <epoch>
  acked = 3
  retained_bytes = 0
  lost = false
  filters:
    area = [0, 0, 0, 0]
    columns = NULL
    column_ids = NULL
    no_before = false
    chunks = false
  lost_reason = NULL
OK
```

## Options

- `--area` uses inclusive chunk coordinates.
- Without `--slot` or `--after`, watching begins after completed writes.
- `--after <epoch:revision>` resumes from a retained position.
- `--slot` resumes from its saved acknowledgement and acknowledges only completely printed eligible events.
- `--ack-every` requires a slot; its default is 1, counting change, chunks and progress events.
- `--columns a,b` projects named columns; `--no-before` omits before rows.
- `--chunks` streams committed chunk versions and binary forms, including a nullable form for deletion.
- `--takeover` requires a slot and replaces its active consumer.
- `--ack-timeout-ms <ms>` requires a slot and a positive mutation acknowledgement deadline.
- `--json` prints one JSON object per line, including before/after rows and event metadata.

Slot names match `[a-z_][a-z0-9_]*` and contain 1–63 bytes.
Creating and dropping slots requires `ADMIN`.
Watching requires `READ`.

CREATE SLOT stores AREA, COLUMNS, NO BEFORE and CHUNKS filters.
Omitted watch filters inherit the slot's saved filters; explicit filters must match or return `INVALID_ARGUMENT reason=slot_filters`.
Changing filters requires a different slot.
`SHOW SLOTS` includes typed filters, stable column IDs and nullable loss reasons.
A renamed column displays its current name; a dropped column retains its ID with a null name and loses the filtered slot.

JSON events include nullable `tag` and `txn`; changes also include `chunk_changes`.
Schema events carry dimensions, block columns and chunk columns.
Progress advances the completed frontier without a mutation.
Progress is ACK-eligible, but never starts or extends the oldest unacknowledged mutation's deadline.
Schema, resync, reserved catalog events and unknown events are not ACK-eligible.
Unknown push kinds are fully decoded and ignored without consuming an ordinary reply.
Resync includes nullable `parent_epoch` and `fork_revision` for lineage checks.

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
