# Named migrations

A file contains named steps, each followed by one supported schema statement.
Use a fresh table for this example:

```bash
cat > migrations.cql <<'CQL'
-- migrate: create_inventory
CREATE TABLE inventory (id u16, name text(32) NULL) CHUNK 2 x 2
-- migrate: add_stock
ALTER TABLE inventory ADD COLUMN stock u16 DEFAULT 0
CQL
./build/chunk-cli --uri "$CHUNKDB_URI" migrate migrations.cql
./build/chunk-cli --uri "$CHUNKDB_URI" migrate migrations.cql
./build/chunk-cli --uri "$CHUNKDB_URI" migrations
```

The first run prints applied for both steps, and the second prints skipped.
Run migrations before application data statements, including at concurrent starts.
Keep each applied name and statement unchanged; add a new named step for later schema changes.
Reusing a name with different text returns CONFLICT.
The command stops at the first error, prints its step and server code, and exits 1; earlier applied steps remain committed.

Names match `[a-z_][a-z0-9_]*`, 1–63 bytes.
Blank lines and full-line `--` comments are ignored.
Multiline statements join trimmed lines with one space; quoted values stay on one line.
Interior spacing and case form part of the migration identity.
No semicolon separators or `$` parameters outside quotes are allowed.
These file rules are checked before connecting; the server checks CQL syntax and rights per step.
Supported steps: CREATE/ALTER/DROP TABLE, GRANT/REVOKE and CREATE/DROP SLOT.
Each step needs its statement's rights even when skipped; listing history needs MANAGES USERS.
`--json` prints `{name,status}` per step, or a history array for `migrations`.
A raw statement such as `MIGRATE 'name' ALTER TABLE ...` is also accepted; lowercase `migrate <file>` selects the file command.
See [server migrations](https://github.com/chunkdb/chunkdb/blob/main/docs/CQL.md#named-migrations) for recovery and limits.

[README](../README.md)
