# chunk-cli

The command-line client for [chunkdb 2.0](https://github.com/chunkdb/chunkdb), a storage engine for tables of chunks containing typed blocks.
It sends [CQL](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md) over protocol 3 and prints replies for people or JSON for scripts.

Install the 2.0 CLI with Go 1.25.6 or newer:

```bash
go install github.com/chunkdb/chunk-cli/v2/cmd/chunk-cli@v2.0.0
```

## Five-minute start

Start a server with the [server quick start](https://github.com/chunkdb/chunkdb/blob/main/docs/QUICK_START.md).
From this repository, with Go 1.25.6 or newer:

```bash
go build -o build/chunk-cli ./cmd/chunk-cli
export CHUNKDB_PASSWORD='your-admin-password'
export CHUNKDB_URI=chunk://admin@127.0.0.1:4242/
./build/chunk-cli --uri "$CHUNKDB_URI" PING
CHUNKCLI_BIN="$PWD/build/chunk-cli" bash examples/world.sh
```

The [world example](examples/world.sh) creates a fresh `world` table with `kind u8` and `name text(16) NULL`, fills four blocks, reads a block and an area with `GET AREA`, then watches an update from grass to a door.
It exits after that change; it needs Bash and an unused `world` table.
To repeat it, first delete only the example's data:

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "DROP TABLE world"
CHUNKCLI_BIN="$PWD/build/chunk-cli" bash examples/world.sh
```

For a single statement, quote the whole CQL string; specify `--uri` on each invocation.
`CHUNKDB_URI` is used by the example script, while the CLI itself defaults to `chunk://127.0.0.1:4242/`.

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" "GET BLOCK 1 1 FROM world"
./build/chunk-cli --uri "$CHUNKDB_URI" --json "GET BLOCK 1 1 FROM world"
./build/chunk-cli --uri "$CHUNKDB_URI" shell
```

The block read prints:

```text
kind = 2
name = 'door'
```

## Commands and guides

All guide examples start from the world table above and use the same `CHUNKDB_URI` and password.

| Feature | Guide |
| --- | --- |
| Options, typed output, chunk files and conditional writes | [Commands](docs/COMMANDS.md) |
| Passwords, TLS and connection errors | [Connection](docs/CONNECTION.md) |
| Users and table rights | [Users](docs/USERS.md) |
| Snapshot reads and atomic writes on one connection | [Transactions](docs/TRANSACTIONS.md) |
| Live changes, resume positions and durable slots | [Feed](docs/FEED.md) |
| Apply named schema steps and list their history | [Migrations](docs/MIGRATIONS.md) |
| Create a server-side backup | [Backup](docs/BACKUP.md) |

Within 2.x, protocol and on-disk changes are additive; chunkdb 2.0 clients and data remain supported by 2.x servers.
Breaking changes wait for 3.0; see the server's [compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).
