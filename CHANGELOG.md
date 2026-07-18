# Changelog

All notable changes to this project will be documented in this file.

This CLI follows [Semantic Versioning](https://semver.org/) and targets the
stable `chunkdb` 1.x protocol; see the engine's
[compatibility policy](https://github.com/chunkdb/chunkdb/blob/main/docs/COMPATIBILITY.md).

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
