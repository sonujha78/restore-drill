#!/usr/bin/env bash
# Local gate. CI runs the same script. Exit 0 = PASS, anything else = FAIL.
set -euo pipefail
cd "$(dirname "$0")/.."

echo "== gofmt"
bad="$(gofmt -l .)"
if [ -n "$bad" ]; then
  echo "gofmt needed on:"
  echo "$bad"
  exit 1
fi

echo "== go vet"
go vet ./...

echo "== go test"
go test ./... -count=1

echo "== go build"
go build -o bin/restore-drill ./cmd/restore-drill

echo "GATE: PASS"
