# Back up server data

Request a backup as a user with `MANAGES USERS`.
The server must have a backup directory configured, and the destination name must be unused or empty.
Use the connection variables from the [README](../README.md) and an unused `world` table name.

1. Store a block and ask the server to copy its data.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8) CHUNK 2 x 2"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 0 0 IN world kind = 1"
chunk-cli --uri "$CHUNKDB_URI" "BACKUP TO 'snapshot'"
```

```output
OK
1
tables = 1
files = 7
bytes = 551
cuts:
  1) table = world, epoch = <epoch>, revision = 1
```

The reply reports table cuts, file counts and copied bytes.
A cut identifies the last included revision, which orders committed changes within a table.
The backup includes table schemas, users and named migration history.

2. Follow [Backup](https://github.com/chunkdb/chunkdb/blob/main/docs/BACKUP.md) to restore the copy with `chunkdb_restore`.
Restore runs offline on the server through a separate tool.
Restored tables receive fresh identities, so existing feed consumers need new positions.

## If it fails

- **Destination refused:** use a relative name under the server's backup directory, outside its live data directory.
- **Invalid path:** use `/` separators; roots, backslashes, `..` and symlinks below the backup directory are refused.
- **Permission denied:** use a login with `MANAGES USERS`.

The path is resolved on the server, independently of your CLI working directory.
See [Backup](https://github.com/chunkdb/chunkdb/blob/main/docs/BACKUP.md) for setup, completion and restore rules.
