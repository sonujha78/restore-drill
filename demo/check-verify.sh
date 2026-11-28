#!/usr/bin/env bash
# Real-data check of the verify layer on the real backups from demo/backup.sh.
# Faults are made in throwaway copies. Prints states and numbers only, never rows.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/restore-drill
CFG=configs/targets.yaml
go build -o "$BIN" ./cmd/restore-drill || { echo "build failed"; exit 3; }
fails=0
ok()   { echo "  ok   $1"; }
bad()  { echo "  FAIL $1"; fails=$((fails+1)); }
info() { echo "  INFO $1"; }

tmp="$(mktemp -d /tmp/rdr-verify-check.XXXXXX)" || exit 3
case "$tmp" in /tmp/rdr-verify-check.??????) ;; *) echo "unexpected temp dir"; exit 3 ;; esac
trap 'rm -rf -- "$tmp"' EXIT

newest() { ls -1 "demo/backups/$1"/$2 2>/dev/null | sort | tail -1; }
tree_hash() { find . \( -path ./.git -o -path ./bin \) -prune -o -type f -printf '%p %s %T@\n' | sort | sha256sum | cut -d' ' -f1; }
mk_sb() { "$BIN" sandbox create --base "$tmp/sbx"; }
before="$(tree_hash)"
notes="$(newest sqlite-notes 'notes-*.db')"
tarf="$(newest files-tar 'files-*.tar.gz')"
[ -n "$notes" ] && [ -n "$tarf" ] || { echo "no real backups, run demo/backup.sh"; exit 3; }

# vcase LABEL TARGET WANT_EXIT PATTERNS [FILE] [NOW]  -> restores into a fresh sandbox, then verifies
vcase() {
  local label="$1" t="$2" want="$3" pats="$4" file="${5:-}" now="${6:-}" sb out code p
  sb="$(mk_sb)" || { bad "$label: sandbox create failed"; return; }
  local r=(restore --config "$CFG" --target "$t" --sandbox "$sb"); [ -n "$file" ] && r+=(--file "$file")
  "$BIN" "${r[@]}" >/dev/null 2>&1 || { bad "$label: restore failed"; return; }
  local v=(verify --config "$CFG" --target "$t" --sandbox "$sb"); [ -n "$now" ] && v+=(--now "$now")
  out="$("$BIN" "${v[@]}" 2>&1)"; code=$?
  printf '%s\n' "$out" >> "$tmp/all.txt"
  LAST_OUT="$out"; LAST_SB="$sb"; LAST_CODE=$code
  if [ "$code" != "$want" ]; then bad "$label: exit $code, want $want"; sed 's/^/         | /' <<<"$out"; return; fi
  while IFS= read -r p; do
    [ -z "$p" ] && continue
    if ! grep -Eq "$p" <<<"$out"; then bad "$label: output lacks /$p/"; sed 's/^/         | /' <<<"$out"; return; fi
  done <<<"$pats"
  ok "$label (exit $code)"
}

echo "== pristine real backups"
vcase "sqlite-notes" sqlite-notes 0 $'overall: pass\npass +integrity\npass +structure:table:notes\npass +content:rows:notes\npass +content:hash:notes\npass +canary\npass +app:no-empty-titles'
sed 's/^/         | /' <<<"$LAST_OUT"
vcase "sqlite-nocanary" sqlite-nocanary 1 $'overall: unverified\nunverified +canary +no canary configured, RPO cannot be measured\nrpo +unknown'
vcase "files-tar" files-tar 0 $'overall: pass\npass +structure:path:data/canary.txt\npass +structure:path:config/app.conf\npass +canary\npass +app:app-conf-has-schema-version'

echo "== worked example on a real canary: canary + 2850 s = RPO 47m30s"
sb="$(mk_sb)"; "$BIN" restore --config "$CFG" --target sqlite-notes --sandbox "$sb" >/dev/null 2>&1
canary="$(python3 -c 'import sqlite3,sys; print(sqlite3.connect("file:"+sys.argv[1]+"?mode=ro",uri=True).execute("select max(written_at) from rdr_canary").fetchone()[0])' "$sb/restored.db")"
plus() { python3 -c 'import sys,datetime; print((datetime.datetime.fromisoformat(sys.argv[1])+datetime.timedelta(seconds=int(sys.argv[2]))).isoformat())' "$canary" "$1"; }
out="$("$BIN" verify --config "$CFG" --target sqlite-notes --sandbox "$sb" --now "$(plus 2850)" 2>&1)"; code=$?
printf '%s\n' "$out" >> "$tmp/all.txt"
if [ "$code" -eq 0 ] && grep -Eq 'rpo +47m30s .*within' <<<"$out"; then ok "RPO 47m30s, within target (canary $canary)"; else bad "worked example: exit $code"; sed 's/^/         | /' <<<"$out"; fi
out="$("$BIN" verify --config "$CFG" --target sqlite-notes --sandbox "$sb" --now "$(plus 18000)" 2>&1)"; code=$?
if [ "$code" -eq 0 ] && grep -Eq 'rpo +5h0m0s .*over' <<<"$out"; then ok "stale (canary + 5h): checks pass, RPO 5h0m0s over target (exit 0)"; else bad "stale: exit $code"; sed 's/^/         | /' <<<"$out"; fi

echo "== faults in copies of the real SQLite backup"
python3 - "$notes" "$tmp" << 'PY'
import sqlite3, sys, shutil
src, d = sys.argv[1], sys.argv[2]
con = sqlite3.connect("file:%s?mode=ro" % src, uri=True)
schema = [r[0] for r in con.execute("select sql from sqlite_master where sql is not null and name not like 'sqlite_%'")]
con.close()
e = sqlite3.connect(d + "/n-empty.db")
for s in schema: e.execute(s)
e.commit(); e.close()
for name, stmt in (("n-nocanarytable.db", "DROP TABLE rdr_canary"), ("n-nobody.db", "ALTER TABLE notes DROP COLUMN body")):
    shutil.copy(src, d + "/" + name)
    c = sqlite3.connect(d + "/" + name); c.execute(stmt); c.commit(); c.close()
PY
vcase "empty but valid (schema, no rows)" sqlite-notes 2 $'overall: fail\nfail +content:rows:notes\nfail +canary +no canary row' "$tmp/n-empty.db"
vcase "missing table rdr_canary" sqlite-notes 2 $'fail +structure:table:rdr_canary' "$tmp/n-nocanarytable.db"
vcase "missing column notes.body" sqlite-notes 2 $'fail +structure:table:notes +missing columns: body' "$tmp/n-nobody.db"
for kind in bitflip truncate; do
  f="$tmp/n-$kind.db"; cp "$notes" "$f"
  if [ "$kind" = truncate ]; then truncate -s "$(( $(stat -c %s "$f") / 2 ))" "$f"; else python3 -c 'import sys; p=sys.argv[1]; b=bytearray(open(p,"rb").read()); b[len(b)//2]^=1; open(p,"wb").write(b)' "$f"; fi
  s="$(mk_sb)"; "$BIN" restore --config "$CFG" --target sqlite-notes --sandbox "$s" --file "$f" >/dev/null 2>&1
  out="$("$BIN" verify --config "$CFG" --target sqlite-notes --sandbox "$s" 2>&1)"; code=$?
  printf '%s\n' "$out" >> "$tmp/all.txt"
  info "$kind: verify exit $code (not asserted, outcome unknown beforehand); $(grep -E ' integrity ' <<<"$out" | sed 's/  */ /g' | cut -c1-150)"
done

echo "== faults in copies of the real tar.gz"
python3 - "$tarf" "$tmp" << 'PY'
import tarfile, sys, io
src, d = sys.argv[1], sys.argv[2]
def rebuild(out, drop=None, edit=None):
    with tarfile.open(src, "r:gz") as tin, tarfile.open(out, "w:gz") as tout:
        for m in tin.getmembers():
            if drop and m.name == drop: continue
            if m.isreg():
                data = tin.extractfile(m).read()
                if edit and m.name == edit[0]:
                    data = edit[1]; m.size = len(data)
                tout.addfile(m, io.BytesIO(data))
            else:
                tout.addfile(m)
rebuild(d + "/files-noconf.tar.gz", drop="config/app.conf")
rebuild(d + "/files-nocanary.tar.gz", drop="data/canary.txt")
rebuild(d + "/files-noschema.tar.gz", edit=("config/app.conf", b"app=rdr-demo\n"))
PY
vcase "tar without config/app.conf" files-tar 2 $'fail +structure:path:config/app.conf' "$tmp/files-noconf.tar.gz"
vcase "tar without data/canary.txt" files-tar 2 $'fail +structure:path:data/canary.txt\nfail +canary +no canary row' "$tmp/files-nocanary.tar.gz"
vcase "tar, app.conf without schema_version" files-tar 2 $'fail +app:app-conf-has-schema-version' "$tmp/files-noschema.tar.gz"

echo "== postgres and refusals"
out="$("$BIN" verify --config "$CFG" --target pg-orders 2>&1)"; code=$?
if [ "$code" -eq 1 ] && grep -q 'not implemented' <<<"$out"; then ok "pg-orders: unverified, not implemented yet (exit 1)"; else bad "pg-orders: exit $code"; fi
out="$("$BIN" verify --config "$CFG" --target sqlite-notes --sandbox "$tmp" 2>&1)"; code=$?
if [ "$code" -eq 3 ] && grep -q refusing <<<"$out"; then ok "plain directory refused (exit 3)"; else bad "plain dir: exit $code"; fi

echo "== no row content, nothing changed"
if grep -Eq 'note [0-9]+|customer [0-9]' "$tmp/all.txt"; then bad "row content found in output"; else ok "no note titles or customer names in any output"; fi
for t in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  if ( cd "demo/backups/$t" && sha256sum -c SHA256SUMS --quiet ) >/dev/null 2>&1; then ok "$t manifest still verifies"; else bad "$t changed"; fi
done
if [ "$(tree_hash)" = "$before" ]; then ok "project tree identical before and after"; else bad "project tree changed"; fi

if [ "$fails" -eq 0 ]; then echo "VERIFY: PASS"; else echo "VERIFY: FAIL ($fails)"; exit 1; fi
