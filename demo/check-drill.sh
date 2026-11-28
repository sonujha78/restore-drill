#!/usr/bin/env bash
# Real-data check of the drill command. It takes FRESH real backups (demo/backup.sh),
# drills them, and drills damaged COPIES (never the real backups) through a copy of
# the config that points at throwaway directories. Needs demo/up.sh.
# Prints states, counts and times only, never rows.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/restore-drill
CFG=configs/targets.yaml
C=rdr-pg
go build -o "$BIN" ./cmd/restore-drill || { echo "build failed"; exit 3; }
fails=0
ok()   { echo "  ok   $1"; }
bad()  { echo "  FAIL $1"; fails=$((fails+1)); }
info() { echo "  INFO $1"; }
want() { if [ "$2" = "$3" ]; then ok "$1 ($2)"; else bad "$1: got '$2', want '$3'"; fi; }
PSQL() { docker exec -i "$C" psql -U rdr -d "$1" -v ON_ERROR_STOP=1 -tA -c "$2"; }

if [ "$(docker inspect -f '{{.State.Health.Status}}' "$C" 2>/dev/null)" != healthy ]; then echo "$C is not healthy, run demo/up.sh"; exit 3; fi

tmp="$(mktemp -d /tmp/rdr-drill-check.XXXXXX)" || exit 3
case "$tmp" in /tmp/rdr-drill-check.??????) ;; *) echo "unexpected temp dir"; exit 3 ;; esac
trap 'rm -rf -- "$tmp"' EXIT

tree_hash() { find . \( -path ./.git -o -path ./bin \) -prune -o -type f -printf '%p %s %T@\n' | sort | sha256sum | cut -d' ' -f1; }
dblist() { PSQL postgres "SELECT datname FROM pg_database ORDER BY 1" | sha256sum | cut -d' ' -f1; }
fp() { PSQL rdrdb "SELECT (SELECT count(*) FROM customers)||'/'||(SELECT count(*) FROM orders)||'/'||(SELECT md5(string_agg(id::text||':'||customer_id::text||':'||amount::text, ',' ORDER BY id)) FROM orders)"; }
newest() { ls -1 "demo/backups/$1"/$2 2>/dev/null | sort | tail -1; }
pat() { case "$1" in pg-orders) echo 'orders-*.dump';; sqlite-notes) echo 'notes-*.db';; files-tar) echo 'files-*.tar.gz';; sqlite-nocanary) echo 'nocanary-*.db';; esac; }
flipbit() { python3 -c 'import sys; p=sys.argv[1]; b=bytearray(open(p,"rb").read()); b[len(b)//2]^=1; open(p,"wb").write(b)' "$1"; }
jget() { python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(eval(sys.argv[2]))' "$1" "$2" 2>/dev/null; }
tstat() { jget "$1" "[t for t in d['targets'] if t['name']=='$2'][0]['$3']"; }
cstat() { jget "$1" "[c for t in d['targets'] if t['name']=='$2' for c in t['checks'] if c['name']=='$3'][0]['status']"; }
nonpass() { jget "$1" "','.join(c['name']+'='+c['status'] for t in d['targets'] if t['name']=='$2' for c in t['checks'] if c['status']!='pass')"; }

# RUN CONFIG [extra drill args]  -> OUT CODE RESULT
RUN() {
  OUT="$("$BIN" drill --config "$1" --out "$tmp/out" --sandbox-base "$tmp/sbx" "${@:2}" 2>&1)"; CODE=$?
  printf '%s\n' "$OUT" >> "$tmp/all.txt"
  RESULT="$(sed -n 's/^result  *//p' <<<"$OUT" | tail -1)"
}

echo "== fresh real backups"
if ./demo/backup.sh >/dev/null 2>&1; then ok "demo/backup.sh took fresh backups"; else bad "demo/backup.sh failed"; echo "DRILL: FAIL ($fails)"; exit 1; fi
before_tree="$(tree_hash)"; before_dbs="$(dblist)"; before_fp="$(fp)"

echo "== full drill of the real backups"
RUN "$CFG"
sed 's/^/         | /' <<<"$OUT"
want "exit code (3 targets verified, sqlite-nocanary unverified)" "$CODE" 1
want "gate decision" "$(jget "$RESULT" "d['gate']['decision']")" degraded
for t in pg-orders sqlite-notes files-tar; do
  want "$t status" "$(tstat "$RESULT" $t status)" verified
  want "$t rto_status" "$(tstat "$RESULT" $t rto_status)" within
  want "$t rpo_status" "$(tstat "$RESULT" $t rpo_status)" within
  info "$t real restore+verify time: $(( $(tstat "$RESULT" $t rto_ns) / 1000000 )) ms, rpo $(( $(tstat "$RESULT" $t rpo_ns) / 1000000000 )) s"
done
want "sqlite-nocanary status" "$(tstat "$RESULT" sqlite-nocanary status)" unverified
want "sqlite-nocanary rpo_status" "$(tstat "$RESULT" sqlite-nocanary rpo_status)" unknown
if grep -q 'RPO cannot be measured' <<<"$OUT"; then ok "the report says RPO cannot be measured"; else bad "no RPO explanation for sqlite-nocanary"; fi
want "pg-orders readable (proven by the restore)" "$(cstat "$RESULT" pg-orders readable)" pass

echo "== the three canary targets alone"
RUN "$CFG" --target pg-orders,sqlite-notes,files-tar
want "exit code" "$CODE" 0
want "gate decision" "$(jget "$RESULT" "d['gate']['decision']")" ok

echo "== damaged copies of the real backups (manifest present)"
# scn NAME TARGET FAULT [nomanifest]  -> SCN_CFG
scn() {
  local name="$1" t="$2" fault="$3" nom="${4:-}" d f src
  d="$tmp/scn/$name"; mkdir -p "$d/backups/$t"
  src="$(newest "$t" "$(pat "$t")")"; f="$d/backups/$t/$(basename "$src")"
  cp "$src" "$f"
  case "$fault" in
    truncate) truncate -s "$(( $(stat -c %s "$f") / 2 ))" "$f" ;;
    bitflip)  flipbit "$f" ;;
    empty)    : > "$f" ;;
    emptyvalid)
      if [ "$t" = pg-orders ]; then
        docker exec "$C" pg_dump -U rdr -d rdrdb -Fc --schema-only > "$f" 2>/dev/null
      else
        rm -f "$f"
        python3 - "$src" "$f" << 'PY'
import sqlite3, sys
src, dst = sys.argv[1], sys.argv[2]
con = sqlite3.connect("file:%s?mode=ro" % src, uri=True)
schema = [r[0] for r in con.execute("select sql from sqlite_master where sql is not null and name not like 'sqlite_%'")]
con.close()
e = sqlite3.connect(dst)
for s in schema: e.execute(s)
e.commit(); e.close()
PY
      fi ;;
  esac
  if [ -z "$nom" ]; then
    if [ "$fault" = emptyvalid ]; then ( cd "$d/backups/$t" && sha256sum "$(basename "$f")" > SHA256SUMS )
    else cp "$(dirname "$src")/SHA256SUMS" "$d/backups/$t/SHA256SUMS"; fi
  fi
  sed "s#dir: demo/backups/#dir: $d/backups/#" "$CFG" > "$d/targets.yaml"
  SCN_CFG="$d/targets.yaml"
}
for fault in truncate bitflip empty; do
  for t in pg-orders sqlite-notes sqlite-nocanary files-tar; do
    scn "$fault-$t" "$t" "$fault"; RUN "$SCN_CFG" --target "$t"
    want "$fault $t exit" "$CODE" 2
    want "$fault $t status" "$(tstat "$RESULT" "$t" status)" failed
    case "$fault" in
      bitflip) want "bitflip $t: checksum caught it" "$(cstat "$RESULT" "$t" checksum)" fail ;;
      empty)   want "empty $t: size caught it" "$(cstat "$RESULT" "$t" size)" fail ;;
      truncate) info "truncate $t: caught by: $(nonpass "$RESULT" "$t")" ;;
    esac
  done
done

echo "== empty but valid (restores fine, holds no data, checksum matches)"
for t in pg-orders sqlite-notes; do
  scn "emptyvalid-$t" "$t" emptyvalid; RUN "$SCN_CFG" --target "$t"
  want "emptyvalid $t exit" "$CODE" 2
  want "emptyvalid $t status" "$(tstat "$RESULT" "$t" status)" failed
  want "emptyvalid $t restore" "$(cstat "$RESULT" "$t" restore)" pass
  want "emptyvalid $t rows check" "$(cstat "$RESULT" "$t" content:rows:$([ "$t" = pg-orders ] && echo orders || echo notes))" fail
  want "emptyvalid $t canary check" "$(cstat "$RESULT" "$t" canary)" fail
done

echo "== bit flip WITHOUT a manifest (what is caught is recorded, but it can never be verified)"
for t in pg-orders sqlite-notes sqlite-nocanary files-tar; do
  scn "bitflip-nomanifest-$t" "$t" bitflip nomanifest; RUN "$SCN_CFG" --target "$t"
  st="$(tstat "$RESULT" "$t" status)"
  if [ "$st" != verified ]; then ok "bitflip $t without manifest is '$st', never verified (exit $CODE)"; else bad "bitflip $t without manifest was reported as verified"; fi
  info "bitflip $t without manifest: not-pass checks: $(nonpass "$RESULT" "$t")"
done

echo "== results and cleanup"
if grep -rEq '/tmp/|/home/' "$tmp/out"; then bad "a result file holds a machine path"; else ok "no machine path in any result file"; fi
if grep -Eq 'example\.test|customer [0-9]|note [0-9]+' "$tmp/all.txt"; then bad "row content found in output"; else ok "no customer names, emails or note titles in any output"; fi
if python3 - "$tmp/out" << 'PY'
import glob, json, sys
files = sorted(glob.glob(sys.argv[1] + "/drill-*.json"))
assert files, "no result files"
for f in files:
    d = json.load(open(f))
    assert d["version"] == 1 and d["targets"] and d["gate"]["decision"] in ("ok", "degraded", "not_restorable"), f
print(len(files))
PY
then ok "every recorded result is valid JSON of the expected shape"; else bad "a result file is malformed"; fi
if [ ! -d "$tmp/sbx" ] || [ -z "$(ls -A "$tmp/sbx")" ]; then ok "no sandbox directory left behind"; else bad "sandbox directories left in $tmp/sbx"; fi
want "pg sandbox databases left" "$(PSQL postgres "SELECT count(*) FROM pg_database WHERE datname LIKE 'rdr\_sbx\_%'")" 0
if [ "$(dblist)" = "$before_dbs" ]; then ok "database list identical"; else bad "database list differs"; fi
if [ "$(fp)" = "$before_fp" ]; then ok "source database fingerprint unchanged ($before_fp)"; else bad "source database changed"; fi
for t in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  if ( cd "demo/backups/$t" && sha256sum -c SHA256SUMS --quiet ) >/dev/null 2>&1; then ok "$t manifest still verifies"; else bad "$t changed"; fi
done
if [ "$(tree_hash)" = "$before_tree" ]; then ok "project tree identical before and after the drills"; else bad "project tree changed"; fi
if ./demo/check-env.sh >/dev/null 2>&1; then ok "env check passes (other containers identical to the snapshot)"; else bad "env check fails"; fi

if [ "$fails" -eq 0 ]; then echo "DRILL: PASS"; else echo "DRILL: FAIL ($fails)"; exit 1; fi
