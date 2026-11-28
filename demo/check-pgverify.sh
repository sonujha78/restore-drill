#!/usr/bin/env bash
# Real-data check of verify for postgres: the real dump is restored into real throwaway
# databases inside rdr-pg and verified. Faults are applied to those throwaway databases
# or come from real pg_dump variants. Needs demo/up.sh and demo/backup.sh.
# Prints states, counts and hashes only, never rows.
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
PSQL() { docker exec -i "$C" psql -U rdr -d "$1" -v ON_ERROR_STOP=1 -tA -c "$2"; }

if [ "$(docker inspect -f '{{.State.Health.Status}}' "$C" 2>/dev/null)" != healthy ]; then echo "$C is not healthy, run demo/up.sh"; exit 3; fi
src_dump="$(ls -1 demo/backups/pg-orders/orders-*.dump 2>/dev/null | sort | tail -1)"
[ -n "$src_dump" ] || { echo "no real pg backup, run demo/backup.sh"; exit 3; }

tmp="$(mktemp -d /tmp/rdr-pgverify-check.XXXXXX)" || exit 3
case "$tmp" in /tmp/rdr-pgverify-check.??????) ;; *) echo "unexpected temp dir"; exit 3 ;; esac
made=()
cleanup() {
  for n in "${made[@]:-}"; do [ -n "$n" ] && "$BIN" pgsandbox remove --container "$C" --db "$n" >/dev/null 2>&1; done
  rm -rf -- "$tmp"
}
trap cleanup EXIT

tree_hash() { find . \( -path ./.git -o -path ./bin \) -prune -o -type f -printf '%p %s %T@\n' | sort | sha256sum | cut -d' ' -f1; }
fp() { PSQL "$1" "SELECT (SELECT count(*) FROM customers)||'/'||(SELECT count(*) FROM orders)||'/'||(SELECT md5(string_agg(id::text||':'||customer_id::text||':'||amount::text, ',' ORDER BY id)) FROM orders)"; }
dblist() { PSQL postgres "SELECT datname FROM pg_database ORDER BY 1" | sha256sum | cut -d' ' -f1; }
newsb() { SB="$("$BIN" pgsandbox create --container "$C" 2>&1)"; local rc=$?; [ $rc -eq 0 ] && made+=("$SB"); return $rc; }
flipbit() { python3 -c 'import sys; p=sys.argv[1]; b=bytearray(open(p,"rb").read()); b[len(b)//2]^=1; open(p,"wb").write(b)' "$1"; }

before_tree="$(tree_hash)"; before_dbs="$(dblist)"; before_fp="$(fp rdrdb)"

# pcase LABEL WANT_EXIT PATTERNS [FILE] [NOW] [FAULT_SQL]
pcase() {
  local label="$1" want="$2" pats="$3" file="${4:-}" now="${5:-}" mod="${6:-}" out code p sb
  newsb || { bad "$label: pgsandbox create failed"; return; }
  sb="$SB"
  local r=(restore --config "$CFG" --target pg-orders --pgdb "$sb"); [ -n "$file" ] && r+=(--file "$file")
  "$BIN" "${r[@]}" >/dev/null 2>&1 || { bad "$label: restore failed"; return; }
  if [ -n "$mod" ]; then PSQL "$sb" "$mod" >/dev/null || { bad "$label: could not apply the fault"; return; }; fi
  local v=(verify --config "$CFG" --target pg-orders --pgdb "$sb"); [ -n "$now" ] && v+=(--now "$now")
  out="$("$BIN" "${v[@]}" 2>&1)"; code=$?
  printf '%s\n' "$out" >> "$tmp/all.txt"
  LAST_OUT="$out"; LAST_SB="$sb"
  if [ "$code" != "$want" ]; then bad "$label: exit $code, want $want"; sed 's/^/         | /' <<<"$out"; return; fi
  while IFS= read -r p; do
    [ -z "$p" ] && continue
    if ! grep -Eq "$p" <<<"$out"; then bad "$label: output lacks /$p/"; sed 's/^/         | /' <<<"$out"; return; fi
  done <<<"$pats"
  ok "$label (exit $code)"
}

echo "== pristine real dump"
pcase "pg-orders pristine" 0 $'overall: pass\npass +structure:table:customers\npass +structure:table:orders\npass +structure:table:rdr_canary\npass +content:rows:customers\npass +content:rows:orders\npass +content:hash:customers\npass +content:hash:orders\npass +canary\npass +app:no-negative-amounts'
sed 's/^/         | /' <<<"$LAST_OUT"
if grep -q ' integrity ' <<<"$LAST_OUT"; then bad "an integrity line appeared for postgres"; else ok "no integrity line for postgres (not supported, restore is the proof)"; fi
pristine_sb="$LAST_SB"

echo "== worked example on the real canary: canary + 2850 s = RPO 47m30s"
canary="$(PSQL "$pristine_sb" "SELECT max(written_at) AT TIME ZONE 'UTC' FROM rdr_canary")"
plus() { python3 -c 'import sys,datetime; print((datetime.datetime.fromisoformat(sys.argv[1]).replace(tzinfo=datetime.timezone.utc)+datetime.timedelta(seconds=int(sys.argv[2]))).isoformat())' "$canary" "$1"; }
out="$("$BIN" verify --config "$CFG" --target pg-orders --pgdb "$pristine_sb" --now "$(plus 2850)" 2>&1)"; code=$?
printf '%s\n' "$out" >> "$tmp/all.txt"
if [ "$code" -eq 0 ] && grep -Eq 'rpo +47m30s .*within' <<<"$out"; then ok "RPO 47m30s, within target (canary $canary UTC)"; else bad "worked example: exit $code"; sed 's/^/         | /' <<<"$out"; fi
out="$("$BIN" verify --config "$CFG" --target pg-orders --pgdb "$pristine_sb" --now "$(plus 18000)" 2>&1)"; code=$?
if [ "$code" -eq 0 ] && grep -Eq 'rpo +5h0m0s .*over' <<<"$out"; then ok "stale (canary + 5h): checks pass, RPO 5h0m0s over target (exit 0)"; else bad "stale: exit $code"; sed 's/^/         | /' <<<"$out"; fi
info "real pristine RPO: $(grep -E '^rpo' <<<"$LAST_OUT")"

echo "== faults"
docker exec "$C" pg_dump -U rdr -d rdrdb -Fc --schema-only > "$tmp/pg-empty.dump" 2>/dev/null
pcase "empty but valid (real schema-only dump)" 2 $'overall: fail\npass +structure:table:orders\nfail +content:rows:orders\nfail +canary +no canary row' "$tmp/pg-empty.dump"
pcase "missing table rdr_canary" 2 $'fail +structure:table:rdr_canary' "" "" "DROP TABLE rdr_canary"
pcase "missing column customers.email" 2 $'fail +structure:table:customers +missing columns: email' "" "" "ALTER TABLE customers DROP COLUMN email"
pcase "orders wiped, canary still there" 2 $'fail +content:rows:orders\npass +canary' "" "" "DELETE FROM orders"
pcase "negative amount (application check)" 2 $'fail +app:no-negative-amounts +returned 1, expected 0' "" "" "UPDATE orders SET amount = -1 WHERE id = 1"

echo "== bit-flipped dump (outcome unknown beforehand, recorded only)"
f="$tmp/pg-bitflip.dump"; cp "$src_dump" "$f"; flipbit "$f"
if newsb; then
  s="$SB"
  if "$BIN" restore --config "$CFG" --target pg-orders --pgdb "$s" --file "$f" >/dev/null 2>&1; then
    out="$("$BIN" verify --config "$CFG" --target pg-orders --pgdb "$s" 2>&1)"; code=$?
    printf '%s\n' "$out" >> "$tmp/all.txt"
    info "bitflip: restore exit 0, verify exit $code, overall: $(grep '^overall' <<<"$out")"
  else
    info "bitflip: restore failed (exit 2 expected from that step)"
  fi
fi

echo "== refusals"
for name in rdrdb postgres template1 rdr_sbx_20261127t100000z_deadbeef; do
  out="$("$BIN" verify --config "$CFG" --target pg-orders --pgdb "$name" 2>&1)"; code=$?
  printf '%s\n' "$out" >> "$tmp/all.txt"
  if [ "$code" -eq 3 ] && grep -q refusing <<<"$out"; then ok "verify --pgdb '$name' refused (exit 3)"; else bad "'$name': exit $code, output: $out"; fi
done
sed 's/container: rdr-pg/container: postgres-other/' "$CFG" > "$tmp/other.yaml"
if grep -q 'container: postgres-other' "$tmp/other.yaml"; then
  out="$("$BIN" verify --config "$tmp/other.yaml" --target pg-orders --pgdb rdr_sbx_20261127t100000z_deadbeef 2>&1)"; code=$?
  if [ "$code" -eq 3 ] && grep -q refusing <<<"$out"; then ok "container postgres-other refused (exit 3)"; else bad "other container: exit $code"; fi
else
  bad "could not build the other-container config"
fi
out="$("$BIN" verify --config "$CFG" --target pg-orders 2>&1)"; code=$?
if [ "$code" -eq 3 ] && grep -q -- '--pgdb' <<<"$out"; then ok "no --pgdb: exit 3 with a hint"; else bad "no --pgdb: exit $code"; fi

echo "== no row content, nothing changed"
if grep -Eq 'example\.test|customer [0-9]|note [0-9]+' "$tmp/all.txt"; then bad "row content found in output"; else ok "no customer names or emails in any output"; fi
if [ "$(fp rdrdb)" = "$before_fp" ]; then ok "source database fingerprint unchanged ($before_fp)"; else bad "source database changed"; fi
for t in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  if ( cd "demo/backups/$t" && sha256sum -c SHA256SUMS --quiet ) >/dev/null 2>&1; then ok "$t manifest still verifies"; else bad "$t changed"; fi
done
if [ "$(tree_hash)" = "$before_tree" ]; then ok "project tree identical before and after"; else bad "project tree changed"; fi
if ./demo/check-env.sh >/dev/null 2>&1; then ok "env check passes (other containers identical to the snapshot)"; else bad "env check fails"; fi

# the sandboxes are removed by the trap; compare the database list after that in a second step
if [ "$fails" -eq 0 ]; then echo "PGVERIFY: PASS"; else echo "PGVERIFY: FAIL ($fails)"; exit 1; fi
