#!/usr/bin/env bash
# Real-data check of the artifact layer. Needs real backups: run demo/backup.sh first.
# Faults are injected into throwaway COPIES, never into the real backups.
# Exit 0 = every expectation met. Prints states and sizes only, never contents.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/restore-drill
CFG=configs/targets.yaml
go build -o "$BIN" ./cmd/restore-drill || { echo "build failed"; exit 3; }

fails=0
ok()  { echo "  ok   $1"; }
bad() { echo "  FAIL $1"; fails=$((fails+1)); }

# run_case LABEL WANT_EXIT PATTERNS(newline separated, may be empty) ARGS...
run_case() {
  local label="$1" want="$2" pats="$3"; shift 3
  local out code p
  out="$("$BIN" artifact --config "$CFG" "$@" 2>&1)"; code=$?
  if [ "$code" != "$want" ]; then
    bad "$label: exit $code, want $want"; sed 's/^/         | /' <<<"$out"; return
  fi
  while IFS= read -r p; do
    [ -z "$p" ] && continue
    if ! grep -Eq "$p" <<<"$out"; then
      bad "$label: output lacks /$p/"; sed 's/^/         | /' <<<"$out"; return
    fi
  done <<<"$pats"
  ok "$label (exit $code)"
}

# Same patterns as configs/targets.yaml, so the script can pick the newest real backup.
pattern_of() {
  case "$1" in
    pg-orders) echo 'orders-*.dump' ;;
    sqlite-notes) echo 'notes-*.db' ;;
    files-tar) echo 'files-*.tar.gz' ;;
    sqlite-nocanary) echo 'nocanary-*.db' ;;
  esac
}
newest() { ls -1 "demo/backups/$1"/$(pattern_of "$1") 2>/dev/null | sort | tail -1; }
flipbit() { python3 -c 'import sys; p=sys.argv[1]; b=bytearray(open(p,"rb").read()); b[len(b)//2]^=1; open(p,"wb").write(b)' "$1"; }

# fault_case TARGET KIND WANT_EXIT PATTERNS [nomanifest]
fault_case() {
  local t="$1" kind="$2" want="$3" pats="$4" nom="${5:-}"
  local src tmp f mf
  src="$(newest "$t")"
  if [ -z "$src" ]; then bad "$t: no real backup found, run demo/backup.sh"; return; fi
  tmp="$(mktemp -d)"; f="$tmp/$(basename "$src")"
  cp "$src" "$f"
  case "$kind" in
    none) ;;
    truncate) truncate -s "$(( $(stat -c %s "$f") / 2 ))" "$f" ;;
    bitflip) flipbit "$f" ;;
    empty) : > "$f" ;;
  esac
  if [ -z "$nom" ]; then cp "$(dirname "$src")/SHA256SUMS" "$tmp/SHA256SUMS"; mf="$tmp/SHA256SUMS"; else mf="$tmp/no-manifest"; fi
  run_case "$t $kind${nom:+, no manifest}" "$want" "$pats" --target "$t" --file "$f" --manifest "$mf"
  rm -rf "$tmp"
}

echo "== pristine real backups"
run_case "sqlite-notes"     0 'overall: pass'       --target sqlite-notes
run_case "sqlite-nocanary"  0 'overall: pass'       --target sqlite-nocanary
run_case "files-tar"        0 'overall: pass'       --target files-tar
run_case "pg-orders"        1 $'overall: unverified\nunverified +readable' --target pg-orders
fault_case sqlite-notes none 1 $'unverified +checksum\noverall: unverified' nomanifest

echo "== truncated (half the file), manifest present"
fault_case pg-orders       truncate 2 $'fail +checksum\nunverified +readable'
fault_case sqlite-notes    truncate 2 $'fail +checksum\nfail +readable'
fault_case sqlite-nocanary truncate 2 $'fail +checksum\nfail +readable'
fault_case files-tar       truncate 2 $'fail +checksum\nfail +readable'

echo "== one flipped bit in the middle, manifest present"
fault_case pg-orders       bitflip 2 $'fail +checksum\nunverified +readable'
fault_case sqlite-notes    bitflip 2 $'fail +checksum\npass +readable'
fault_case sqlite-nocanary bitflip 2 $'fail +checksum\npass +readable'
fault_case files-tar       bitflip 2 $'fail +checksum\nfail +readable'

echo "== empty file"
for t in pg-orders sqlite-notes sqlite-nocanary files-tar; do
  fault_case "$t" empty 2 'fail +size'
done

echo "== one flipped bit, NO manifest (what is caught without a known hash)"
fault_case files-tar    bitflip 2 $'fail +readable\nunverified +checksum' nomanifest
fault_case sqlite-notes bitflip 1 $'pass +readable\nunverified +checksum\noverall: unverified' nomanifest
fault_case pg-orders    bitflip 1 $'unverified +checksum\noverall: unverified' nomanifest

echo "== real backups untouched by all of the above"
for t in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  if ( cd "demo/backups/$t" && sha256sum -c SHA256SUMS --quiet ) >/dev/null 2>&1; then ok "$t manifest still verifies"; else bad "$t: a real backup changed"; fi
done

if [ "$fails" -eq 0 ]; then echo "ARTIFACTS: PASS"; else echo "ARTIFACTS: FAIL ($fails)"; exit 1; fi
