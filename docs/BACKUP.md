# Server-side backups

Start the server with `--backup-dir` pointing to a directory on its filesystem.
As a user with MANAGES USERS:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "BACKUP TO 'snapshot'"
```

The relative name is resolved under the server's backup directory, independently of the CLI's working directory.
Use `/` between components; roots, backslashes, `..` and symlinks below that directory are refused.
The destination must be absent or empty and outside the live data directory.
The reply lists table cuts, file counts and bytes copied.
A completed backup includes table schemas, users and named migration history.
Restore is an offline server tool, `chunkdb_restore`, which gives the copy fresh identities; it is not a CLI command.
See the server's [backup guide](https://github.com/chunkdb/chunkdb/blob/main/docs/BACKUP.md) for completion, restore and consumer behavior.

[README](../README.md)
