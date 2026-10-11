# Apply named schema changes

A migration is a named schema change that the server applies once.
Use the connection variables from the [README](../README.md) and an unused `inventory` table name.

1. Write a file with one named step per schema statement.

```sh docs-check-shell
cat > migrations.cql <<'CQL'
-- migrate: create_inventory
CREATE TABLE inventory (id u16, name text(32) NULL) CHUNK 2 x 2
-- migrate: add_stock
ALTER TABLE inventory ADD COLUMN stock u16 DEFAULT 0
CQL
chunk-cli --uri "$CHUNKDB_URI" migrate migrations.cql
```

```output
create_inventory applied
add_stock applied
```

2. Run the same file again.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" migrate migrations.cql
```

```output
create_inventory skipped
add_stock skipped
```

3. Inspect the history.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" migrations
```

```output
1) name = create_inventory, applied_ms = <time-ms>, user = admin, statement = CREATE TABLE inventory (id u16, name text(32) NULL) CHUNK 2 x 2
2) name = add_stock, applied_ms = <time-ms>, user = admin, statement = ALTER TABLE inventory ADD COLUMN stock u16 DEFAULT 0
```

`<time-ms>` replaces generated timestamps in checked output.
Run migrations before application statements, including when applications start concurrently.
Keep applied names and statement text unchanged.
Add a new named step for later changes.

## File rules

- Names match `[a-z_][a-z0-9_]*` and contain 1–63 bytes.
- Blank lines and full-line `--` comments are ignored.
- Multiline statements join trimmed lines with one space; quoted values stay on one line.
- Interior spacing and case form part of the migration identity.
- Semicolon separators and `$` parameters outside quotes are refused.

Supported steps are CREATE/ALTER/DROP TABLE, GRANT/REVOKE and CREATE/DROP SLOT.
Each step requires its statement's rights even when skipped.
Listing history requires `MANAGES USERS`.

## If it fails

- **CONFLICT:** an applied name has different text; restore the original step and add a new name.
- **File error:** fix the reported line before connecting.
- **Server error:** fix the failing step; earlier applied steps remain committed.

The command stops at the first error, prints its step and server code, and exits 1.
`--json` prints `{name,status}` per step or a history array for `migrations`.

Raw `MIGRATE 'name' <statement>` also works.
Lowercase `migrate <file>` selects the file command.
See [Migrations](https://github.com/chunkdb/chunkdb/blob/main/docs/reference/MIGRATIONS.md) for statement syntax and limits.
