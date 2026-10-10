#!/usr/bin/env bash
# Run against a server from the repository root: bash examples/world.sh
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
uri=${CHUNKDB_URI:-chunk://admin@127.0.0.1:4242/}
work=$(mktemp -d "${TMPDIR:-/tmp}/chunk-cli-world.XXXXXX")
watch_pid=''
cleanup() {
  if [[ -n "$watch_pid" ]]; then
    kill -INT "$watch_pid" 2>/dev/null || true
    wait "$watch_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

cli=${CHUNKCLI_BIN:-$work/chunk-cli}
if [[ -z "${CHUNKCLI_BIN:-}" ]]; then
  (cd "$root" && go build -o "$cli" ./cmd/chunk-cli)
fi
command=("$cli" --uri "$uri")
if [[ "${CHUNKDB_TLS_INSECURE:-0}" == 1 ]]; then
  command+=(--tls-insecure)
fi

"${command[@]}" "CREATE TABLE world (kind u8, name text(16) NULL) CHUNK 2 x 2"
for y in 0 1; do
  for x in 0 1; do
    "${command[@]}" "SET BLOCK $x $y IN world kind = 1, name = 'grass'"
  done
done
"${command[@]}" "GET BLOCK 0 0 FROM world"
"${command[@]}" --blocks "GET AREA 0 0 TO 0 0 FROM world"

# The start reply confirms WATCH is active before the update is sent.
mkfifo "$work/events"
"${command[@]}" --json watch world > "$work/events" &
watch_pid=$!
exec 3< "$work/events"
if ! IFS= read -r -t 15 start <&3 || [[ "$start" != *'"type":"start"'* ]]; then
  echo 'world example: WATCH did not start' >&2
  exit 1
fi
printf '%s\n' "$start"
"${command[@]}" "SET BLOCK 1 1 IN world kind = 2, name = 'door'"
while IFS= read -r -t 15 event <&3; do
  printf '%s\n' "$event"
  if [[ "$event" == *'"type":"change"'* ]]; then
    kill -INT "$watch_pid"
    wait "$watch_pid"
    watch_pid=''
    exec 3<&-
    exit 0
  fi
done
echo 'world example: WATCH did not report the update' >&2
exit 1
