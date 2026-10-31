# Contributing

Use the [organization contribution guide](https://github.com/chunkdb/.github/blob/main/CONTRIBUTING.md) for reporting issues and proposing changes.

## Local checks

Use the Go toolchain declared in `go.mod`.
Run these checks from the repository root:

```bash
gofmt -l .
go vet ./...
go test -race ./...
go build ./...
```

`gofmt -l .` must print no paths.
Integration tests and checked documentation examples need a server built with TLS support:

```bash
CHUNKDB_SERVER_BIN=/path/to/chunkdb/build/chunkdb_server go test -race -count=1 ./...
```

Without `CHUNKDB_SERVER_BIN`, those tests skip.
Update `docs/` with command behavior, flags, exit codes, and observable output when they change.
