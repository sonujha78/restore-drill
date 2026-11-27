#!/usr/bin/env bash
# Real-data check of the PostgreSQL restore: the real dump from demo/backups is
# restored into a real throwaway database inside rdr-pg. Needs demo/up.sh and demo/backup.sh.
# Prints counts, hashes and states only, never rows.
set -uo pipefail
cd "$(dirname "$0")/.."

BIN=./bin/restore-drill
C=rdr-pg
go build -o "$BIN" ./cmd/restore-drill || { echo "build failed"; exit 3; }
fails=0
ok()   { echo "  ok   $1"; }
bad()  { echo "  FAIL $1"; fails=$((fails+1)); }
info() { echo "  INFO $1"; }
PSQL() { docker exec -i "$C" psql -U rdr -d "$1" -v ON_ERROR_STOP=1 -tA -c "$2"; }

if [ "$(docker inspect -f '{{.State.Health.Status}}' "$C" 2>/dev/null)" != healthy ]; then echo "$C is not healthy, run demo/up.sh"; exit 3; fi
src_dump="$(ls -1 demo/backups/pg-orders/orders-*.dump 2>/dev/null | sort | tail -1)"
[ -n "$src_dump" ] || { echo "no real pg backup, run demo/backup.sh"; exit 3; }

tmp="$(mktemp -d /tmp/rdr-pgrestore-check.XXXXXX)" || exit 3
case "$tmp" in /tmp/rdr-pgrestore-check.??????) ;; *) echo "unexpected temp dir"; exit 3 ;; esac
made=()
cleanup() {
  for n in "${made[@]:-}"; do [ -n "$n" ] && "$BIN" pgsandbox remove --container "$C" --db "$n" >/dev/null 2>&1; done
  rm -rf -- "$tmp"
}
trap cleanup EXIT

tree_hash() { find . \( -path ./.git -o -path ./bin \) -prune -o -type f -printf '%p %s %T@\n' | sort | sha256sum | cut -d' ' -f1; }
fp() { PSQL "$1" "SELECT (SELECT count(*) FROM customers)||'/'||(SELECT count(*) FROM orders)||'/'||(SELECT md5(string_agg(id::text||':'||customer_id::text||':'||amount::text, ',' ORDER BY id)) FROM orders)"; }
dblist() { PSQL postgres "SELECT datname FROM pg_database ORDER BY 1" | sha256sum | cut -d' ' -f1; }
flipbit() { python3 -c 'import sys; p=sys.argv[1]; b=bytearray(open(p,"rb").read()); b[len(b)//2]^=1; open(p,"wb").write(b)' "$1"; }
newsb() { SB="$("$BIN" pgsandbox create --container "$C" 2>&1)"; local rc=$?; [ $rc -eq 0 ] && made+=("$SB"); return $rc; }
rmsb()  { "$BIN" pgsandbox remove --container "$C" --db "$1" >/dev/null 2>&1; }
run_restore() { out="$("$BIN" restore --config configs/targets.yaml --target pg-orders --pgdb "$@" 2>&1)"; c=$?; printf '%s\n' "$out" >> "$tmp/all.txt"; }

before_tree="$(tree_hash)"
before_dbs="$(dblist)"
before_fp="$(fp rdrdb)"
info "source fingerprint before (customers/orders/md5): $before_fp"

echo "== real restore into a throwaway database"
newsb || { bad "pgsandbox create failed: $SB"; echo "PGRESTORE: FAIL ($fails)"; exit 1; }
sb="$SB"
if [[ "$sb" =~ ^rdr_sbx_[0-9]{8}t[0-9]{6}z_[0-9a-f]{8}$ ]]; then ok "sandbox database name has the expected form"; else bad "unexpected name '$sb'"; fi
if "$BIN" pgsandbox check --container "$C" --db "$sb" >/dev/null 2>&1; then ok "check accepts it"; else bad "check rejected it"; fi
run_restore "$sb"
sed 's/^/         | /' <<<"$out"
if [ "$c" -eq 0 ]; then ok "restore exit 0"; else bad "restore exit $c"; fi
got_fp="$(fp "$sb")"
if [ "$got_fp" = "$before_fp" ]; then ok "restored customers/orders/md5 equal the source ($got_fp)"; else bad "restored $got_fp, source $before_fp"; fi
tables="$(PSQL "$sb" "SELECT count(*) FROM information_schema.tables WHERE table_schema='public'")"
if [ "$tables" = 3 ]; then ok "3 tables restored"; else bad "$tables tables restored, want 3"; fi
info "canary rows: restored $(PSQL "$sb" 'SELECT count(*) FROM rdr_canary'), source $(PSQL rdrdb 'SELECT count(*) FROM rdr_canary') (not asserted)"
if rmsb "$sb" && [ "$(PSQL postgres "SELECT count(*) FROM pg_database WHERE datname='$sb'")" = 0 ]; then ok "sandbox database removed"; else bad "sandbox database still exists"; fi

echo "== damaged copies of the real dump"
for kind in truncate bitflip empty; do
  f="$tmp/pg-$kind.dump"; cp "$src_dump" "$f"
  case "$kind" in
    truncate) truncate -s "$(( $(stat -c %s "$f") / 2 ))" "$f" ;;
    bitflip)  flipbit "$f" ;;
    empty)    : > "$f" ;;
  esac
  newsb || { bad "$kind: pgsandbox create failed"; continue; }
  s="$SB"
  run_restore "$s" --file "$f"
  case "$kind" in
    bitflip) info "bitflip: restore exit $c (not asserted, outcome unknown beforehand)" ;;
    *) if [ "$c" -eq 2 ]; then ok "$kind: restore failed with exit 2"; else bad "$kind: exit $c, want 2"; fi ;;
  esac
  docker exec -i "$C" pg_restore --list < "$f" >/dev/null 2>&1; info "$kind: pg_restore --list exit $?"
  rmsb "$s"
done

echo "== refusals (source database must stay untouched)"
for name in rdrdb postgres template1 'x; DROP DATABASE rdrdb' rdr_sbx_20261127t100000z_deadbeef; do
  run_restore "$name"
  if [ "$c" -eq 3 ] && grep -q refusing <<<"$out"; then ok "restore into '$name' refused (exit 3)"; else bad "'$name': exit $c, output: $out"; fi
done
out="$("$BIN" pgsandbox remove --container "$C" --db rdrdb 2>&1)"; c=$?
if [ "$c" -eq 3 ] && [ "$(PSQL postgres "SELECT count(*) FROM pg_database WHERE datname='rdrdb'")" = 1 ]; then ok "remove of rdrdb refused, rdrdb still exists"; else bad "remove rdrdb: exit $c"; fi
out="$("$BIN" pgsandbox create --container postgres-other 2>&1)"; c=$?
if [ "$c" -eq 3 ] && grep -q refusing <<<"$out"; then ok "container postgres-other refused before any docker command (exit 3)"; else bad "other container: exit $c"; fi

echo "== no row content in any output"
if grep -Eq 'example\.test|customer [0-9]' "$tmp/all.txt"; then bad "row content found in output"; else ok "no customer names or emails in any output"; fi

echo "== everything else untouched"
if [ "$(fp rdrdb)" = "$before_fp" ]; then ok "source database fingerprint unchanged"; else bad "source database changed"; fi
if [ "$(dblist)" = "$before_dbs" ]; then ok "database list identical (no leftover sandboxes)"; else bad "database list differs"; fi
for t in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  if ( cd "demo/backups/$t" && sha256sum -c SHA256SUMS --quiet ) >/dev/null 2>&1; then ok "$t manifest still verifies"; else bad "$t changed"; fi
done
if [ "$(tree_hash)" = "$before_tree" ]; then ok "project tree identical before and after"; else bad "project tree changed"; fi
if ./demo/check-env.sh >/dev/null 2>&1; then ok "env check passes (other containers identical to the snapshot)"; else bad "env check fails"; fi

if [ "$fails" -eq 0 ]; then echo "PGRESTORE: PASS"; else echo "PGRESTORE: FAIL ($fails)"; exit 1; fi
