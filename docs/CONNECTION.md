# Connect to chunkdb

Supply your server address, user and password before sending statements.
Start a server using [Quick start](https://github.com/chunkdb/chunkdb/blob/main/docs/QUICK_START.md).

1. Set the connection variables.

```sh
export CHUNKDB_URI=chunk://admin@127.0.0.1:4242/
export CHUNKDB_PASSWORD='your-admin-password'
```

2. Check the connection.

```sh docs-check-shell
chunk-cli --uri "$CHUNKDB_URI" PING
```

```output
PONG
```

3. To use a password file, write its first line and connect.

```sh docs-check-shell
printf '%s\n' "$CHUNKDB_PASSWORD" > admin.password
chunk-cli --uri "$CHUNKDB_URI" --user admin --password-file admin.password PING
```

```output
PONG
```

`--user` overrides the URI's user.
Password precedence is `--password-file`, URI password, `CHUNKDB_PASSWORD`, then a terminal prompt without echo.
URI credentials use percent escapes for `:`, `@`, `/` and `%`.
The CLI uses SCRAM-SHA-256 to prove password knowledge without sending the password itself.

## TLS

Use `chunks://` for a TLS server and `chunk://` for plain TCP.
TLS protects statements and replies.
For a certificate with a different name, set `--tls-server-name <name>`.
`--tls-insecure` accepts an untrusted certificate, including local self-signed certificates.

The default network timeout is `5s`.
Use `--timeout 10s` to change it.
One-shot statements and interactive shell statements are not retried.
[Transaction files](TRANSACTIONS.md#transaction-files) retry known retryable errors unless `--no-retry` is set.

## Session options

Use repeated `--hello-option key=value` flags before the command.
For example, `--hello-option client_name=importer` names the connection.
`statement_timeout_ms` accepts unsigned milliseconds; other flag values are sent as bytes.
`default_tag` accepts at most 256 bytes.

HELLO reports applied options in `options` and unapplied options, including unknown names, in `not_applied`.
Currently, `client_name` is applied; `statement_timeout_ms` and `default_tag` report `implementation_pending`.
Unapplied options do not change statement behavior.
`features` lists supported protocol capabilities; it does not list enabled storage features.
For storage features and their raw masks, use `SHOW STATUS` with `ADMIN` on `*`.

## If it fails

- **Connection refused:** check the URI, host, port and server availability.
- **TLS error:** check the URI scheme and certificate settings.
- **AUTH_REQUIRED:** supply a user and password.
- **AUTH_FAILED:** check the supplied credentials.

Without a user, the connection succeeds only when the server disables authentication.
For example, omitting the user from the connection URI prints this error and exits 1:

```sh docs-check-error
chunk-cli --uri "${CHUNKDB_URI/admin@/}" PING
```

```output
error: connecting failed: AUTH_REQUIRED reason=auth_required use HELLO 3 USER <name> $1 with a SCRAM-SHA-256 client-first message; set the username and password in your client connection URI (log in with --user or chunk://user:password@host/)
```
Failed commands preserve the server's error code and metadata.
See [error exit codes](COMMANDS.md#errors) before deciding whether to retry.
See [Commands](COMMANDS.md) for all options.

## Watch reconnection

`watch` reconnects after a transport failure using the same login, TLS settings, filters and slot.
It resumes `AFTER` the last completely printed change, chunk event, progress frontier or resync position.
Until an event is printed, the initial watch start position is used.
An incomplete frame or failed output write never advances the position.
Each successful attachment prints another `start` event.

Five consecutive reconnect attempts use delays of 25, 50, 100, 200 and 400 milliseconds.
Printing a new frontier resets this limit.
`watch <table> --no-reconnect` exits on the first lost connection.
After a transport failure, `SLOT_BUSY` also uses the bounded reconnect attempts while the old consumer detaches, without taking over another consumer.
Other server refusals, malformed frames and output errors stop the watch without reconnecting.
Ctrl-C interrupts reconnection waits and closes an active watch with its final ACK and UNWATCH.

An ordinary watch can resume only while the server retains its position.
A `resync` event still requires rebuilding application state as described in [Feed](FEED.md).
Use a durable slot when the position must survive a server restart.
