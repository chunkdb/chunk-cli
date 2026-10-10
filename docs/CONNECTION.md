# Connect to chunkdb 2.0

The CLI opens protocol 3 connections with SCRAM-SHA-256 authentication.
Set a user in the URI or with `--user`; `--user` overrides the URI's user.
The password comes from `--password-file` (first line), then the URI, then `CHUNKDB_PASSWORD`, then a terminal prompt without echo.
Without a user, only a server running with `--auth none` accepts the connection.

```bash
./build/chunk-cli --uri "$CHUNKDB_URI" PING
./build/chunk-cli --uri "$CHUNKDB_URI" --user admin --password-file admin.password "SHOW TABLES"
```

Create `admin.password` with the server's admin password before the second command.
URI credentials use percent escapes for special characters such as `:`, `@`, `/` and `%`.
Authentication proves password knowledge without sending the password itself.
TLS protects the rest of the connection.

```bash
./build/chunk-cli --uri chunks://admin@127.0.0.1:4242/ --tls-insecure PING
```

Use `chunks://` for a server configured with TLS; `chunk://` selects plain TCP.
`--tls-insecure` accepts an untrusted certificate and is intended for local self-signed certificates.
For a trusted certificate with a different name, set `--tls-server-name <name>`.
The default network timeout is `5s`; `--timeout 10s` changes it.

A refused connection points to the URI, host, port and server availability.
A TLS failure points to the scheme and certificate settings.
`AUTH_REQUIRED` asks for a user and password; `AUTH_FAILED` asks you to check them.
Server errors preserve their code and message; failed commands exit 1.
The CLI does not retry statements or reconnect a watch automatically.

[Commands](COMMANDS.md) · [README](../README.md)
