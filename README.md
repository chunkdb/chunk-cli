# chunk-cli

[![CI](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml)

The command-line client for [chunkdb](https://github.com/chunkdb/chunkdb), which stores typed blocks in chunks.
A block is one cell with named values.
A chunk groups blocks under one version, which changes when its stored data changes.

## Store and read a block

Start a server using [Quick start](https://github.com/chunkdb/chunkdb/blob/main/docs/QUICK_START.md), then set your connection details.

```sh
export CHUNKDB_PASSWORD='your-admin-password'
export CHUNKDB_URI=chunk://admin@127.0.0.1:4242/
```

With an unused `world` table name:

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8, name text(16) NULL) CHUNK 2 x 2"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 2, name = 'door'"
chunk-cli --uri "$CHUNKDB_URI" "GET BLOCK 1 1 FROM world"
```

```output
OK
2
kind = 2
name = 'door'
```

Each write prints the chunk's version.
For several statements on one connection, run `chunk-cli --uri "$CHUNKDB_URI" shell`.

## Install

You need Go 1.25.6 or newer.

```sh
go install github.com/chunkdb/chunk-cli/v2/cmd/chunk-cli@latest
```

Add Go's binary directory to your `PATH` if `chunk-cli` is not found.
See [Connection](docs/CONNECTION.md) for passwords and TLS.

## Guides

- [Commands](docs/COMMANDS.md): options, output and chunk files.
- [Transactions](docs/TRANSACTIONS.md): read and write on one connection.
- [Feed](docs/FEED.md): watch changes and resume a durable consumer.
- [Users](docs/USERS.md): passwords and table rights.
- [Migrations](docs/MIGRATIONS.md): apply named schema changes.
- [Backup](docs/BACKUP.md): copy server data.
- [CQL reference](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md): statement syntax.

Examples are recorded against the current server build and checked by the CLI tests.
