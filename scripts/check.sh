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

echo "== real file: validate configs/targets.yaml"
./bin/restore-drill validate --config configs/targets.yaml

echo "== real file: a config with a secret must be rejected with exit 3 and no leak"
tmp="$(mktemp)"
printf 'targets:\n  - name: x\n    password: hunter2\n' > "$tmp"
set +e
out="$(./bin/restore-drill validate --config "$tmp" 2>&1)"
code=$?
set -e
rm -f "$tmp"
if [ "$code" -ne 3 ]; then
  echo "expected exit 3 for a bad config, got $code"
  exit 1
fi
case "$out" in
  *hunter2*)
    echo "secret leaked into the output"
    exit 1
    ;;
esac

echo "GATE: PASS"
