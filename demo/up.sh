#!/usr/bin/env bash
# Start the demo Postgres (rdr-pg, 127.0.0.1:19432) and seed it.
# Touches only rdr- named things. Safe to run again.
set -euo pipefail
cd "$(dirname "$0")/.."

IMG="postgres:16-alpine"
PORT=19432
mkdir -p .rdr

snap_others() {
  { docker ps -a --format '{{.Names}}' | grep -v '^rdr-' || true; } | sort \
    | xargs -r docker inspect -f '{{.Name}} {{.Id}} {{.State.StartedAt}} {{.State.Status}}'
}

# 1. Record what else is on this machine BEFORE we do anything.
if [ ! -f .rdr/others-before.txt ]; then
  snap_others > .rdr/others-before.txt
fi

# 2. Image: record whether this project pulled it, so cleanup removes only that.
if docker image inspect "$IMG" >/dev/null 2>&1; then
  grep -q "$IMG" .rdr/images.txt 2>/dev/null || echo "PREEXISTING $IMG" >> .rdr/images.txt
else
  docker pull "$IMG"
  echo "PULLED $IMG" >> .rdr/images.txt
fi

# 3. Generated password, never written anywhere except the gitignored .env.
if [ ! -f .env ]; then
  ( umask 077
    printf 'RDR_PG_PASSWORD=%s\n' "$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')" > .env )
fi

# 4. Refuse to start if the port is taken by something that is not our container.
if ! docker inspect rdr-pg >/dev/null 2>&1; then
  if [ -n "$(ss -ltnH "sport = :$PORT")" ]; then
    echo "port $PORT is already in use, refusing to start"
    exit 1
  fi
fi

docker compose -f demo/docker-compose.yml --env-file .env up -d

st=""
for _ in $(seq 1 60); do
  st="$(docker inspect -f '{{.State.Health.Status}}' rdr-pg 2>/dev/null || true)"
  [ "$st" = "healthy" ] && break
  sleep 2
done
[ "$st" = "healthy" ] || { echo "rdr-pg did not become healthy (last status: $st)"; exit 1; }

docker exec -i rdr-pg psql -U rdr -d rdrdb -v ON_ERROR_STOP=1 -q < demo/seed/pg.sql
echo "rdr-pg is up on 127.0.0.1:$PORT and seeded"
