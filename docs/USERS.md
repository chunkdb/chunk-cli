# Users and rights

User administration requires `MANAGES USERS`; a user may change their own password.
CREATE/ALTER USER PASSWORD is CLI syntax: it computes a SCRAM verifier from `--new-password-file` or a repeated terminal prompt.

```bash
printf '%s\n' 'bot-example-password' > bot.password
./build/chunk-cli --uri "$CHUNKDB_URI" --new-password-file bot.password "CREATE USER bot PASSWORD"
./build/chunk-cli --uri "$CHUNKDB_URI" "GRANT READ ON world TO bot"
./build/chunk-cli --uri chunk://bot@127.0.0.1:4242/ --password-file bot.password "GET BLOCK 1 1 FROM world"
./build/chunk-cli --uri "$CHUNKDB_URI" "SHOW USERS"
./build/chunk-cli --uri "$CHUNKDB_URI" "REVOKE READ ON world FROM bot"
./build/chunk-cli --uri "$CHUNKDB_URI" "DROP USER bot"
```

READ permits reading/watching, WRITE adds mutation, and ADMIN adds table/schema/slot administration.
A table without a matching right appears as `NO_TABLE`.
A forbidden operation on a visible table returns `PERMISSION_DENIED`.
Grants can also use `*` for all tables; see the server's [user guide](https://github.com/chunkdb/chunkdb/blob/main/docs/USERS.md).

[Connection](CONNECTION.md) · [README](../README.md)
