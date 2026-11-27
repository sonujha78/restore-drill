#!/usr/bin/env bash
# Real-data check of restore: real backups from demo/backups, real escape attempts.
# Needs: demo/backup.sh run before. Prints states and counts only.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/restore-drill
go build -o "$BIN" ./cmd/restore-drill || { echo "build failed"; exit 3; }
fails=0
ok()  { echo "  ok   $1"; }
bad() { echo "  FAIL $1"; fails=$((fails+1)); }

tmp="$(mktemp -d /tmp/rdr-restore-check.XXXXXX)" || exit 3
case "$tmp" in /tmp/rdr-restore-check.??????) ;; *) echo "unexpected temp dir"; exit 3 ;; esac
trap 'rm -rf -- "$tmp"' EXIT

tree_hash() { find . \( -path ./.git -o -path ./bin \) -prune -o -type f -printf '%p %s %T@\n' | sort | sha256sum | cut -d' ' -f1; }
before="$(tree_hash)"
newest() { ls -1 "demo/backups/$1"/$2 2>/dev/null | sort | tail -1; }
mk_sb() { "$BIN" sandbox create --base "$tmp/sbx"; }

echo "== real tar.gz restore"
sb="$(mk_sb)"
out="$("$BIN" restore --config configs/targets.yaml --target files-tar --sandbox "$sb" 2>&1)"; c=$?
if [ "$c" -eq 0 ] && grep -q 'restored 25 files' <<<"$out"; then :; fi
echo "$out" | sed 's/^/         | /'
if [ "$c" -eq 0 ]; then ok "restore exit 0"; else bad "restore exit $c"; fi
want_files="$(tar -tzf "$(newest files-tar 'files-*.tar.gz')" | grep -vc '/$')"
got_files="$(find "$sb" -type f ! -name .rdr-sandbox | wc -l)"
if [ "$want_files" = "$got_files" ]; then ok "sandbox holds $got_files files, same as the archive"; else bad "sandbox holds $got_files files, archive has $want_files"; fi
if [ -f "$sb/config/app.conf" ] && grep -q schema_version "$sb/config/app.conf"; then ok "config/app.conf restored with schema_version"; else bad "config/app.conf missing"; fi
if [ -f "$sb/data/canary.txt" ]; then ok "canary file restored"; else bad "canary file missing"; fi

echo "== real SQLite restore"
sb2="$(mk_sb)"
"$BIN" restore --config configs/targets.yaml --target sqlite-notes --sandbox "$sb2" >/dev/null 2>&1; c=$?
src="$(newest sqlite-notes 'notes-*.db')"
if [ "$c" -eq 0 ] && [ "$(sha256sum < "$sb2/restored.db")" = "$(sha256sum < "$src")" ]; then ok "restored.db is byte-identical to the backup"; else bad "sqlite restore differs (exit $c)"; fi

echo "== corrupted tar copies fail to restore"
for kind in truncate bitflip; do
  d="$tmp/$kind"; mkdir -p "$d"; f="$d/files-corrupt.tar.gz"
  cp "$(newest files-tar 'files-*.tar.gz')" "$f"
  if [ "$kind" = truncate ]; then truncate -s "$(( $(stat -c %s "$f") / 2 ))" "$f"; else python3 -c 'import sys; p=sys.argv[1]; b=bytearray(open(p,"rb").read()); b[len(b)//2]^=1; open(p,"wb").write(b)' "$f"; fi
  s="$(mk_sb)"
  "$BIN" restore --config configs/targets.yaml --target files-tar --sandbox "$s" --file "$f" >/dev/null 2>&1; c=$?
  if [ "$c" -eq 2 ]; then ok "$kind: restore failed with exit 2"; else bad "$kind: exit $c, want 2"; fi
done

echo "== escape attempts on real tar files"
python3 - "$tmp" << 'PY'
import sys, tarfile, io
d = sys.argv[1]
def make(name, adds):
    with tarfile.open(f"{d}/{name}.tar.gz", "w:gz") as t:
        for ti, data in adds:
            t.addfile(ti, io.BytesIO(data) if data is not None else None)
def reg(n, data=b"x"):
    ti = tarfile.TarInfo(n); ti.size = len(data); return ti, data
make("dotdot", [reg("../escaped-dotdot.txt")])
make("absolute", [reg(d + "/escaped-abs.txt")])
sl = tarfile.TarInfo("lnk"); sl.type = tarfile.SYMTYPE; sl.linkname = d
make("symlink", [(sl, None), reg("lnk/escaped-sym.txt")])
hl = tarfile.TarInfo("hl"); hl.type = tarfile.LNKTYPE; hl.linkname = "a"
make("hardlink", [reg("a"), (hl, None)])
PY
for n in dotdot absolute symlink hardlink; do
  s="$(mk_sb)"
  "$BIN" restore --config configs/targets.yaml --target files-tar --sandbox "$s" --file "$tmp/$n.tar.gz" >/dev/null 2>&1; c=$?
  if [ "$c" -eq 2 ]; then ok "$n: refused (exit 2)"; else bad "$n: exit $c, want 2"; fi
done
if ls "$tmp"/escaped-* "$tmp"/sbx/escaped-* >/dev/null 2>&1; then bad "an escaped file exists outside a sandbox"; else ok "no escaped-* file anywhere outside the sandboxes"; fi

echo "== non-sandbox is refused"
plain="$tmp/plain"; mkdir -p "$plain"
"$BIN" restore --config configs/targets.yaml --target files-tar --sandbox "$plain" >/dev/null 2>&1; c=$?
if [ "$c" -eq 3 ] && [ -z "$(ls -A "$plain")" ]; then ok "plain directory refused (exit 3), nothing written"; else bad "plain dir: exit $c"; fi
"$BIN" restore --config configs/targets.yaml --target files-tar --sandbox . >/dev/null 2>&1; c=$?
if [ "$c" -eq 3 ]; then ok "project root refused (exit 3)"; else bad "project root: exit $c"; fi

echo "== real backups and project tree untouched"
for t in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  if ( cd "demo/backups/$t" && sha256sum -c SHA256SUMS --quiet ) >/dev/null 2>&1; then ok "$t manifest still verifies"; else bad "$t changed"; fi
done
if [ "$(tree_hash)" = "$before" ]; then ok "project tree identical before and after"; else bad "project tree changed"; fi

if [ "$fails" -eq 0 ]; then echo "RESTORE: PASS"; else echo "RESTORE: FAIL ($fails)"; exit 1; fi
