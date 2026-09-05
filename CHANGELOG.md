# Changelog

All notable changes to this project will be documented in this file.

This CLI follows [Semantic Versioning](https://semver.org/) and targets the
stable `chunkdb` 1.x protocol; see the engine's
[compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

## Unreleased

### Fixed
- the usage text printed by `help` / `--help` gave `chunksetbin <cx> <cy> <hex>
  | --in <file>`, a form the argument parser rejects: `--in` has to precede the
  coordinates. It now shows both accepted forms, and lists `help` itself among
  the commands

## 1.2.0 - 2026-09-03

### Added
- `chunksetbin` and `chunksetbinstate`: binary chunk writes over the new
  `CHUNKSETBIN` command (chunkdb server 1.3+). The payload is given as hex or
  read from a file with `--in`, in the byte layouts `chunkbin` /
  `chunkbinstate` print, so `chunkbin --out` output can be written back as is.
  Also available in `shell`

## 1.1.0 - 2026-07-18

### Added
- World-read commands: `chunkscan`, `chunkrange`, `chunkradius`.
- Chunk concurrency commands: `chunkver`, `chunkcas`, `chunkbatch`.
- `walflush` explicit durability barrier command.
- `chunkbinc` / `chunkbincstate` zrle-compressed binary chunk transfer with a
  bounded local decoder.

Each new command validates its arguments before sending. Existing commands and
their output are unchanged.

## 1.0.0

Initial stable release: point and chunk-level commands (`get`/`set`/`exists`/
`unset`, `chunkexists`/`chunk`/`chunkset`/`chunkbin` and their `state` forms),
`mset`/`mget` batch commands, `info`, `metrics`, and the interactive `shell`.
