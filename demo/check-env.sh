#!/usr/bin/env bash
# Checks the demo environment against the expected facts. Exit 0 = PASS.
# Prints counts and states only, never data rows and never the password.
set -uo pipefail
cd "$(dirname "$0")/.."

fails=0
ok()  { echo "  ok   $1"; }
bad() { echo "  FAIL $1"; fails=$((fails+1)); }
expect_eq() { if [ "$2" = "$3" ]; then ok "$1 ($2)"; else bad "$1: got '$2', want '$3'"; fi; }
insp() { local v; v="$(docker inspect -f "$1" rdr-pg 2>/dev/null | head -1)"; echo "${v:-missing}"; }
q() { docker exec -i rdr-pg psql -U rdr -d rdrdb -tA -c "$1" 2>/dev/null || echo "error"; }

snap_others() {
  { docker ps -a --format '{{.Names}}' | grep -v '^rdr-' || true; } | sort \
    | xargs -r docker inspect -f '{{.Name}} {{.Id}} {{.State.StartedAt}} {{.State.Status}}'
}

echo "== container"
expect_eq "rdr-pg state" "$(insp '{{.State.Status}}')" "running"
expect_eq "rdr-pg health" "$(insp '{{.State.Health.Status}}')" "healthy"
expect_eq "port binding" "$(docker port rdr-pg 5432/tcp 2>/dev/null | head -1)" "127.0.0.1:19432"
expect_eq "containers carrying our label" \
  "$(docker ps -a --filter label=com.restore-drill.project=restore-drill --format '{{.Names}}' | tr '\n' ' ')" "rdr-pg "

echo "== seed data (counts only)"
expect_eq "customers" "$(q 'SELECT count(*) FROM customers')" "120"
expect_eq "orders" "$(q 'SELECT count(*) FROM orders')" "600"
expect_eq "negative amounts" "$(q 'SELECT count(*) FROM orders WHERE amount < 0')" "0"
canaries="$(q 'SELECT count(*) FROM rdr_canary')"
if [ "$canaries" != "error" ] && [ "$canaries" -ge 1 ] 2>/dev/null; then ok "canary rows ($canaries)"; else bad "canary rows: got '$canaries', want >= 1"; fi

echo "== other projects untouched"
if [ -f .rdr/others-before.txt ]; then
  if diff <(snap_others) .rdr/others-before.txt >/dev/null; then ok "other containers identical to the snapshot taken before start"
  else bad "other containers differ from the snapshot (restart or change), investigate"; fi
else
  bad "no snapshot .rdr/others-before.txt (run demo/up.sh first)"
fi

echo "== secrets"
if [ -f .env ]; then
  pw="$(cut -d= -f2- .env)"
  if [ -n "$pw" ] && grep -rqF --exclude-dir=.git --exclude-dir=.rdr --exclude=.env -e "$pw" . 2>/dev/null; then
    bad "the generated password appears in a file outside .env"
  else
    ok "password appears only in .env"
  fi
  expect_eq ".env permissions" "$(stat -c %a .env)" "600"
  if git check-ignore -q .env; then ok ".env is gitignored"; else bad ".env is NOT gitignored"; fi
else
  bad ".env missing (run demo/up.sh first)"
fi

if [ "$fails" -eq 0 ]; then echo "ENV: PASS"; else echo "ENV: FAIL ($fails)"; exit 1; fi
