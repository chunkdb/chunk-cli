# Watch changes with a durable slot

A watch streams committed block changes; `watch --catalog` streams [catalog changes](#watch-the-catalog).
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
1
OK
```

2. Start watching and wait for the `start` line before writing in another terminal.

```sh docs-check-watch
chunk-cli --uri "$CHUNKDB_URI" watch world --slot consumer --ack-every 10 --area 0,0,0,0
```

```output
start <epoch>:1
schema <epoch>:2 version 1
change revision 2 time_ms <time-ms> user admin
  block 1 1: {kind = 1} -> {kind = 6}
```

The stream may include periodic `progress <epoch>:<revision>` lines before or after the illustrated change, including while idle.

3. In the other terminal, write the change shown above.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 6"
```

```output
2
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
  acked = 2
  revision = 2
  lag_revisions = 0
  retained_bytes = 0
  attached = false
  peer = NULL
  last_ack_ms = NULL
  lost = false
  lost_reason = NULL
  filters:
    area = [0, 0, 0, 0]
    column_ids = NULL
    columns = NULL
    no_before = false
    chunks = false
  owner = admin
OK
```

## Options

- `--area` uses inclusive chunk coordinates; repeat it to watch up to 64 areas with the same axes; a slot keeps one area.
- Three-axis tables use `--area cx0,cy0,cz0,cx1,cy1,cz1`; the third axis remains explicit when zero.
- Without `--slot` or `--after`, watching begins after completed writes.
- `--after <epoch:revision>` resumes from a retained position.
- `--slot` resumes from its saved acknowledgement and acknowledges only completely printed eligible events.
- `--ack-every` requires a slot; its default is 1, counting change, chunks and progress events.
- `--columns a,b` projects block columns in the requested order; chunk metadata remains complete. `--no-before` omits before rows.
- `--chunks` streams committed chunk versions and binary forms, including a nullable form for deletion.
- Chunk-column changes appear in chunk change metadata; binary chunk forms retain the complete chunk-column row.
- `--takeover` requires a slot and replaces its active consumer on the first attach; reconnects never take the slot over.
- `--ack-timeout-ms <ms>` requires a slot and a positive mutation acknowledgement deadline.
- `--json` prints one JSON object per line, including before/after rows and event metadata.

Slot names match `[a-z_][a-z0-9_]*` and contain 1–63 bytes.
Creating and dropping slots requires `ADMIN`.
Watching requires `READ`; watching another user's slot also requires `ADMIN` on `*`.

CREATE SLOT stores AREA, COLUMNS, NO BEFORE and CHUNKS filters.
`OWNER <user>` sets the slot's owner; it defaults to the user running CREATE SLOT, and the user need not exist yet.
Under `--auth none`, CREATE SLOT without OWNER records no owner.
CREATE SLOT IF NOT EXISTS with another OWNER returns `INVALID_ARGUMENT reason=slot_filters`.
Omitted watch filters inherit the slot's saved filters; explicit filters must match or return `INVALID_ARGUMENT reason=slot_filters`.
Changing filters requires a different slot.
`SHOW SLOTS` includes typed filters, stable column IDs, nullable loss reasons and the owner.
For an attached watch, `peer` is the client's `[address]:port` as shown by `SHOW CONNECTIONS`, and `last_ack_ms` is the time in milliseconds since the watch's last accepted ACK.
Both fields are `NULL` when the slot is detached.
`last_ack_ms` is also `NULL` before the watch's first accepted ACK.
The server shows `peer` to the watch's user and to clients allowed to list all connections, and returns `NULL` to other viewers.
The owner is shown to that user, to users who manage users and to everyone on a server started with `--auth none`, and is `NULL` otherwise.
A renamed column displays its current name; a dropped column retains its ID with a null name and loses the filtered slot.

JSON events include nullable `tag` and `txn`; changes also include `chunk_changes`.
Schema events carry dimensions, block columns and chunk columns.
Progress advances the completed frontier without a mutation.
Progress is ACK-eligible, but never starts or extends the oldest unacknowledged mutation's deadline.
Schema, resync, catalog and unknown events are not ACK-eligible.
Unknown push kinds are fully decoded and ignored without consuming an ordinary reply.
Resync includes nullable `parent_epoch` and `fork_revision` for lineage checks.

## Watch the catalog

`watch --catalog` streams changes to tables, users and slots with WATCH CATALOG.
It needs `MONITOR` or `ADMIN` on `*`; users and slot owners appear only with `MANAGES USERS`.

```sh
chunk-cli --uri "$CHUNKDB_URI" watch --catalog
```

```text
start <epoch>:<revision>
catalog revision <revision> time_ms <time-ms>
  create table world
  create user admin
  create slot consumer on world
catalog revision <revision> time_ms <time-ms> user admin
  drop slot consumer on world
```

The first event holds the whole catalog: start your copy from it.
Each later event prints one `action kind name` line per change, with `on <table>` for a slot, and names the user whose statement made it.
`--json` prints each event with the `data` of each change: the DESCRIBE map of a table, the SHOW USERS map of a user, and a slot's filters and owner.
A `resync` line means the watch fell behind; the whole catalog follows it, so start your copy over from it.
`--catalog` takes only `--json` and `--no-reconnect`; a reconnect starts again from the whole catalog.
See the server's [WATCH CATALOG](https://github.com/chunkdb/chunkdb/blob/main/docs/reference/FEED.md#watch-catalog) reference for the event rules.

## If it fails

- **resync:** rebuild state using [Resume a consumer](https://github.com/chunkdb/chunkdb/blob/main/docs/FEED_RESUME.md).
- **SLOT_LOST:** rebuild consumer state and replace the slot.
- **Connection lost:** the CLI reconnects from the last completely printed position, with up to five consecutive reconnect attempts; `--no-reconnect` stops on the first loss.
- **Blocked output:** cancellation sends `UNWATCH`, but exit waits for the output write to return.

A crash can repeat printed changes whose acknowledgements are not durable yet.
Changes finishing output after cancellation starts are not acknowledged.
Stdout does not confirm downstream storage.
For exactly-once processing, use a client that stores data and position together before acknowledging.

See [Change feed](https://github.com/chunkdb/chunkdb/blob/main/docs/CHANGE_FEED.md) for recovery rules.
