# chunk-cli

[![CI](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml)

The command-line client for [chunkdb](https://github.com/chunkdb/chunkdb), which stores typed blocks in chunks.
A block is one cell with named values.
A chunk groups blocks under one version, which changes when its stored data changes.

## World in five minutes

Start a server using [Quick start](https://github.com/chunkdb/chunkdb/blob/main/docs/QUICK_START.md), [install](#install) `chunk-cli`, then set your connection details.
With the Docker quick start, the password is the `Generated password` line of `docker logs chunkdb-quickstart`.

```sh
export CHUNKDB_PASSWORD='your-admin-password'
export CHUNKDB_URI=chunk://admin@127.0.0.1:4242/
```

1. Create a `world_cli` table with a named migration.

```sh docs-check-shell
cat > world.cql <<'CQL'
-- migrate: create_world_cli
CREATE TABLE world_cli (kind u8, name text(16) NULL) CHUNK 2 x 2
CQL
chunk-cli --uri "$CHUNKDB_URI" migrate world.cql
chunk-cli --uri "$CHUNKDB_URI" migrate world.cql
```

```output
create_world_cli applied
create_world_cli skipped
```

The second run skips the applied step, so migrations can run at every start.

2. Write two blocks, then read one block and the chunk that holds them.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 0 0 IN world_cli kind = 1, name = 'grass'"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world_cli kind = 2, name = 'door'"
chunk-cli --uri "$CHUNKDB_URI" "GET BLOCK 1 1 FROM world_cli"
chunk-cli --uri "$CHUNKDB_URI" "GET AREA 0 0 TO 0 0 FROM world_cli"
```

```output
1
2
kind = 2
name = 'door'
chunk 0 0
  version = 2
  schema_version = 1
  commit_time_ms = <time-ms>
  present = 2 of 4 blocks
```

Each write prints the chunk's version; `--blocks` before `GET AREA` also prints the blocks.

3. Watch the table, and wait for the `start` line.

```sh docs-check-watch
chunk-cli --uri "$CHUNKDB_URI" watch world_cli
```

```output
start <epoch>:2
change revision 3 time_ms <time-ms> user admin
  block 0 0: {kind = 1, name = 'grass'} -> {kind = 3, name = 'stone'}
```

4. In another terminal with the same variables, change a block.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 0 0 IN world_cli kind = 3, name = 'stone'"
```

```output
3
```

The watcher prints the change; press Ctrl-C to stop it.
`<epoch>` and `<time-ms>` stand for generated values, and [`examples/world.sh`](examples/world.sh) runs these steps as one script.

## Install

```sh
go install github.com/chunkdb/chunk-cli/v2/cmd/chunk-cli@latest
```

You need Go 1.25.6 or newer; add Go's binary directory to your `PATH` if `chunk-cli` is not found.

## Guides

- [Connection](docs/CONNECTION.md): passwords, TLS and connection errors.
- [Commands](docs/COMMANDS.md): options, output, the shell and chunk files.
- [Transactions](docs/TRANSACTIONS.md): read and write on one connection.
- [Feed](docs/FEED.md): watch changes and resume a durable consumer.
- [Users](docs/USERS.md): passwords and table rights.
- [Migrations](docs/MIGRATIONS.md): apply named schema changes.
- [Backup](docs/BACKUP.md): copy server data.
- [CQL reference](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md): statement syntax.
