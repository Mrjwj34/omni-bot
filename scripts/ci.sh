#!/usr/bin/env bash
set -euo pipefail

require_race=0
for arg in "$@"; do
  case "$arg" in
    --require-race) require_race=1 ;;
    *)
      echo "unknown argument: $arg" >&2
      exit 2
      ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

echo "==> gofmt"
mapfile -t go_files < <(
  find . \
    \( -name .git -o -name data -o -name node_modules -o -name .svelte-kit -o -name build -o -name dist -o -name bin \) -prune \
    -o -name '*.go' -type f -print
)
if [[ ${#go_files[@]} -gt 0 ]]; then
  unformatted="$(gofmt -l "${go_files[@]}")"
  if [[ -n "$unformatted" ]]; then
    echo "Unformatted Go files:" >&2
    echo "$unformatted" >&2
    exit 1
  fi
fi

echo "==> go vet"
go vet ./...

if command -v gcc >/dev/null 2>&1 || command -v clang >/dev/null 2>&1 || command -v cc >/dev/null 2>&1; then
  echo "==> go test -race"
  CGO_ENABLED=1 go test -race ./...
elif [[ "$require_race" -eq 1 ]]; then
  echo "No C compiler found in PATH; cannot run go test -race" >&2
  exit 1
else
  echo "WARN: no C compiler found; running go test ./... without -race" >&2
  echo "==> go test"
  go test ./...
fi

echo "==> checkreadme"
go run ./scripts/checkreadme

if [[ -f web/package.json ]]; then
  echo "==> web check"
  pnpm --dir web check
  echo "==> web build"
  pnpm --dir web build
else
  echo "==> web check/build [skipped: web/package.json not found]"
fi

echo "CI passed."
