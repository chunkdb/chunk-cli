# Changes and durable slots

Start a watch after the README's world example; run the write in another terminal after the `start` line appears:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" watch world --area 0,0,0,0 --json
```

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 6"
```

`--area` uses inclusive chunk coordinates.
Without `--after`, watching starts after completed writes; `--after <epoch:revision>` resumes from a retained position.
JSON is one object per line with `type` and `position`; changes include `commit_time_ms`, user, schema version and before/after rows.
Text output uses CQL literals and `(absent)` for a missing row.
Schema events describe version changes; rows are decoded with their recorded schema.
A `resync` event means the retained position is unavailable: keep consuming while rereading state on another connection, apply only changes newer than each chunk's version, and keep the reported frontier with the rebuilt state before applying later changes.

## Durable consumer

Create a slot before the changes it should retain, then start watching:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "CREATE SLOT 'consumer' ON world"
./build/chunk-cli --uri "$CHUNKDB_URI" watch world --slot consumer --ack-every 10 --json
```

In another terminal, write a change:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 7"
```

After the change has printed, press Ctrl-C; inspect its acknowledgement and remove the example slot:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "SHOW SLOTS ON world"
./build/chunk-cli --uri "$CHUNKDB_URI" "DROP SLOT 'consumer' ON world"
```

CREATE/DROP SLOT need ADMIN on the table; WATCH needs READ.
Slot names match `[a-z_][a-z0-9_]*`, 1–63 bytes.
`--slot` resumes from the written acknowledgement and ACKs only completely printed changes.
The default is one ACK per change; `--ack-every` requires a slot and batches changes, while schema events do not count.
Ctrl-C flushes a completed partial batch and sends UNWATCH; changes finishing output after cancellation starts are not acknowledged.
UNWATCH can be sent while output is blocked, but exit waits for that output write to return.
Lost connections/errors exit nonzero without automatic reconnect; `SLOT_LOST` requires rebuilding consumer state and replacing the slot.
A crash can repeat printed events because ACKs may not yet be durable.
Stdout does not confirm downstream storage; for exactly-once processing, use a client that commits output and position together before ACK.
See [server feed rules](https://github.com/chunkdb/chunkdb/blob/main/docs/CHANGE_FEED.md).

[README](../README.md)
