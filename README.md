# chunk-cli

Minimal practical CLI client for interacting with the `chunk` database.

`chunk-cli` is a standalone Go project and repository, focused on real terminal workflows against `chunk://` and `chunks://` endpoints.

## Features

- supports `chunk://` and `chunks://` connections
- commands:
  - `ping`
  - `info`
  - `auth`
  - `get`
  - `set`
  - `chunk`
  - `chunkbin`
- automatic auth for non-`auth` commands when token is present in URI or `--token`
- readable terminal output and explicit error messages

## Installation

Requirements:

- Go 1.22+ (tested with modern Go toolchains)

Install from source (current checkout):

```bash
go build -o chunk-cli ./cmd/chunk-cli
./chunk-cli version
```

Install to your `GOBIN`:

```bash
go install ./cmd/chunk-cli
```

## Usage

```bash
chunk-cli [global options] <command> [command args]
```

Global options:

- `--uri <chunk://token@host:port/ | chunks://token@host:port/>`
- `--token <token>`
- `--timeout <duration>`
- `--tls-insecure`
- `--tls-server-name <name>`

Defaults:

- URI default: `chunk://127.0.0.1:4242/`
- timeout default: `5s`

## Command Examples

Assume a local server:

```bash
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ ping
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ info
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ get 0 0
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ set 0 0 1111000011110000
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunk 0 0
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkbin 0 0
./chunk-cli --uri chunk://127.0.0.1:4242/ auth mytoken
```

Write raw `CHUNKBIN` payload to file:

```bash
./chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkbin --out chunk_0_0.bin 0 0
```

TLS endpoint (self-signed cert example):

```bash
./chunk-cli --uri chunks://mytoken@127.0.0.1:4242/ --tls-insecure info
```

## Output Behavior

- `ping`, `auth`, `set`: prints simple server text (`PONG`, `OK`, etc.)
- `info`, `get`, `chunk`: prints returned text payload
- `chunkbin`: prints byte count + hex dump by default
- `chunkbin --out <file>`: writes raw bytes to file and prints a concise summary

## Error Behavior

`chunk-cli` exits with non-zero status on failures and prints errors in a direct form:

- connection errors
- TLS errors
- URI/argument validation errors
- server protocol errors (`-ERR ...`)

## Development

Run tests:

```bash
go test ./...
```

Run directly:

```bash
go run ./cmd/chunk-cli --help
```
