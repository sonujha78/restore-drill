#!/usr/bin/env bash
# Real-data check of the sandbox safety rules on the real filesystem and the real demo backups.
# Read-only attempts (check) may target real directories, including / and $HOME.
# Destructive attempts (remove) only ever target throwaway decoys made here.
# Exit 0 = every expectation met.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/restore-drill
go build -o "$BIN" ./cmd/restore-drill || { echo "build failed"; exit 3; }

fails=0
ok()  { echo "  ok   $1"; }
bad() { echo "  FAIL $1"; fails=$((fails+1)); }

tmp="$(mktemp -d /tmp/rdr-sandbox-check.XXXXXX)" || exit 3
case "$tmp" in
  /tmp/rdr-sandbox-check.??????) ;;
  *) echo "unexpected temp dir name"; exit 3 ;;
esac
trap 'rm -rf -- "$tmp"' EXIT

tree_hash() {
  find . \( -path ./.git -o -path ./bin \) -prune -o -type f -printf '%p %s %T@\n' | sort | sha256sum | cut -d' ' -f1
}
before="$(tree_hash)"

echo "== create"
base="$tmp/base"
sb1="$("$BIN" sandbox create --base "$base" 2>&1)"; c=$?
if [ "$c" -ne 0 ] || [ ! -d "$sb1" ]; then
  bad "sandbox create failed (exit $c): $sb1"
  echo "SANDBOX: FAIL ($fails)"
  exit 1
fi
ok "sandbox created"
sb2="$("$BIN" sandbox create --base "$base" 2>&1)"
if [ -d "$sb2" ] && [ "$sb1" != "$sb2" ]; then ok "two sandboxes get different names"; else bad "second create gave '$sb2'"; fi
if [ "$(stat -c %a "$sb1")" = "700" ]; then ok "directory mode 700"; else bad "directory mode is $(stat -c %a "$sb1"), want 700"; fi
if [ -f "$sb1/.rdr-sandbox" ]; then ok "marker file present"; else bad "marker file missing"; fi
if "$BIN" sandbox check --dir "$sb1" >/dev/null 2>&1; then ok "check accepts a real sandbox"; else bad "check rejected a real sandbox"; fi

echo "== check refuses real directories (read-only)"
expect_refused() {
  local label="$1" dir="$2" out code
  if [ ! -d "$dir" ]; then bad "$label: directory does not exist, nothing to test"; return; fi
  out="$("$BIN" sandbox check --dir "$dir" 2>&1)"; code=$?
  if [ "$code" -eq 3 ] && grep -q "refusing" <<<"$out" && grep -q "not a restore-drill sandbox" <<<"$out"; then
    ok "$label refused"
  else
    bad "$label: exit $code, output: $out"
  fi
}
expect_refused "project root"    .
expect_refused "real backups"    demo/backups/files-tar
expect_refused "demo source"     demo/source
expect_refused "/tmp"            /tmp
expect_refused "home directory"  "$HOME"
expect_refused "/"               /
expect_refused "a plain temp dir" "$tmp"

echo "== remove refuses decoys (decoys only, never real directories)"
expect_remove_refused() {
  local label="$1" dir="$2" out code kept=NO
  out="$("$BIN" sandbox remove --dir "$dir" 2>&1)"; code=$?
  [ -f "$dir/keep.txt" ] && kept=yes
  if [ "$code" -eq 3 ] && grep -q "refusing" <<<"$out" && [ "$kept" = yes ]; then
    ok "$label: remove refused, file kept"
  else
    bad "$label: exit $code, file kept: $kept, output: $out"
  fi
}
mkdir -p "$tmp/decoy-plain" "$tmp/decoy-named/rdr-sbx-fake"
echo keep > "$tmp/decoy-plain/keep.txt"
echo keep > "$tmp/decoy-named/rdr-sbx-fake/keep.txt"
expect_remove_refused "plain directory" "$tmp/decoy-plain"
expect_remove_refused "named rdr-sbx-fake, no marker" "$tmp/decoy-named/rdr-sbx-fake"

sb3="$("$BIN" sandbox create --base "$base" 2>&1)"
if [ -d "$sb3" ]; then
  echo keep > "$sb3/keep.txt"; rm -f "$sb3/.rdr-sandbox"
  expect_remove_refused "sandbox whose marker was deleted" "$sb3"
else
  bad "third create failed: $sb3"
fi
sb4="$("$BIN" sandbox create --base "$base" 2>&1)"
if [ -d "$sb4" ]; then
  echo keep > "$sb4/keep.txt"; printf 'tampered\n' > "$sb4/.rdr-sandbox"
  expect_remove_refused "sandbox whose marker was overwritten" "$sb4"
else
  bad "fourth create failed: $sb4"
fi

echo "== remove works on a real sandbox"
out="$("$BIN" sandbox remove --dir "$sb1" 2>&1)"; code=$?
if [ "$code" -eq 0 ] && [ ! -e "$sb1" ]; then ok "real sandbox removed"; else bad "remove: exit $code, output: $out"; fi
if [ -d "$sb2" ]; then ok "the other sandbox was not touched"; else bad "the other sandbox disappeared"; fi

echo "== nothing outside the temp dir changed"
if [ "$(tree_hash)" = "$before" ]; then ok "project tree identical before and after"; else bad "project tree changed"; fi

if [ "$fails" -eq 0 ]; then echo "SANDBOX: PASS"; else echo "SANDBOX: FAIL ($fails)"; exit 1; fi
