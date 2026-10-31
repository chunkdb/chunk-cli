# Give a user read access

Create a user and grant access to one table.
You need a login with `MANAGES USERS` and the connection variables from the [README](../README.md).
Use unused `world` and `bot` names.

1. Create the table and the user's password file.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "CREATE TABLE world (kind u8) CHUNK 2 x 2"
chunk-cli --uri "$CHUNKDB_URI" "SET BLOCK 1 1 IN world kind = 2"
printf '%s\n' 'bot-example-password' > bot.password
chunk-cli --uri "$CHUNKDB_URI" --new-password-file bot.password "CREATE USER bot PASSWORD"
chunk-cli --uri "$CHUNKDB_URI" "GRANT READ ON world TO bot"
```

```output
OK
1
OK
OK
```

The CLI computes a password verifier from `--new-password-file` or a repeated terminal prompt.
The same options work with `ALTER USER bot PASSWORD`.
A user may change their own password.

2. Read as the new user.

```sh docs-check-shell
bot_uri=${CHUNKDB_URI/admin@/bot@}
chunk-cli --uri "$bot_uri" --password-file bot.password "GET BLOCK 1 1 FROM world"
```

```output
kind = 2
```

`READ` permits reading and watching.
`WRITE` also permits mutation.
`ADMIN` also permits table, schema and slot administration.
Grants may use `*` for all tables.

3. Remove access and delete the example user.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" "REVOKE READ ON world FROM bot"
chunk-cli --uri "$CHUNKDB_URI" "DROP USER bot"
```

```output
OK
OK
```

## If it fails

- **NO_TABLE:** the table is absent or your user lacks a matching right.
- **PERMISSION_DENIED:** your user sees the table but cannot perform the operation.
- **AUTH_FAILED:** check the user's password file.

See [Users](https://github.com/chunkdb/chunkdb/blob/main/docs/USERS.md) for server rules and [Connection](CONNECTION.md) for login options.
