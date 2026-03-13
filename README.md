# chunk-cli

[![CI](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml/badge.svg)](https://github.com/chunkdb/chunk-cli/actions/workflows/ci.yml)

`chunk-cli` is the first practical command-line client for `chunkdb`, a specialized chunk/grid storage engine.

It provides direct terminal access to the chunk protocol for operational checks, debugging, and scripting.

## Features

- connection URIs:
  - `chunk://` (plain TCP)
  - `chunks://` (TLS)
- core commands:
  - `ping`
  - `info`
  - `auth`
  - `get`
  - `set`
  - `chunk`
  - `chunkbin`
- token auth via URI (`chunk://token@host:port/`) or `--token`
- clear text output and explicit error messages

## Installation

Requirements:

- Go `1.25+`

Build from source:

```bash
go build -o chunk-cli ./cmd/chunk-cli
./chunk-cli version
```

Install into `GOBIN`:

```bash
go install ./cmd/chunk-cli
```

## Quick Start

Default URI is `chunk://127.0.0.1:4242/`.

```bash
chunk-cli ping
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ info
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ get 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ set 0 0 10110011
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunk 0 0
chunk-cli --uri chunk://mytoken@127.0.0.1:4242/ chunkbin 0 0
```

## Usage

```bash
chunk-cli [global options] <command> [command args]
```

Global options:

- `--uri <chunk://token@host:port/ | chunks://token@host:port/>`
- `--token <token>`
- `--timeout <duration>` (default: `5s`)
- `--tls-insecure` (for self-signed TLS in `chunks://` mode)
- `--tls-server-name <name>`

Auth behavior:

- for non-`auth` commands, CLI auto-runs `AUTH` when token is present in URI or `--token`
- for `auth`, token is taken from `auth <token>` first, otherwise from URI/`--token`

## Command Reference

- `ping`
  - sends `PING`, expects simple response (`+PONG`)
- `info`
  - sends `INFO`, prints returned bulk text
- `auth <token>`
  - sends `AUTH <token>`, prints simple response
- `get <x> <y>`
  - sends `GET`, prints block bit payload
- `set <x> <y> <bits>`
  - sends `SET`; validates `bits` as binary (`0`/`1`) before request
- `chunk <cx> <cy>`
  - sends `CHUNK`, prints text chunk payload
- `chunkbin [--out <file>] <cx> <cy>`
  - sends `CHUNKBIN`
  - default output: payload size + hex dump
  - with `--out`: writes raw bytes to file and prints summary

## TLS (`chunks://`) Example

```bash
chunk-cli --uri chunks://mytoken@127.0.0.1:4242/ --tls-insecure info
```

## Output and Errors

- normal responses are printed in readable form (text commands preserve server text; `chunkbin` includes byte count)
- errors are printed as `error: ...` and process exits non-zero
- server `-ERR ...` responses are surfaced directly

## Development

Run local checks:

```bash
gofmt -w .
go vet ./...
go test ./...
go build ./...
```

Show help:

```bash
go run ./cmd/chunk-cli --help
```
