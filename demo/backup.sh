#!/usr/bin/env bash
# Take one real backup of every demo target.
# Writes only under demo/source and demo/backups. Each backup is preceded by a
# canary write, the way a real system would mark its newest data.
set -euo pipefail
cd "$(dirname "$0")/.."

TS="$(date -u +%Y%m%dT%H%M%SZ)"
SRC=demo/source
BK=demo/backups
mkdir -p "$SRC" "$BK/pg-orders" "$BK/sqlite-notes" "$BK/files-tar" "$BK/sqlite-nocanary"

add_manifest() { ( cd "$1" && sha256sum "$2" >> SHA256SUMS ); }

# 1. PostgreSQL: pg_dump runs inside rdr-pg, the file lands on the host.
docker exec -i rdr-pg psql -U rdr -d rdrdb -v ON_ERROR_STOP=1 -q -c "INSERT INTO rdr_canary (written_at) VALUES (now());"
f="orders-$TS.dump"
if ! docker exec rdr-pg pg_dump -U rdr -d rdrdb -Fc > "$BK/pg-orders/$f"; then
  rm -f "$BK/pg-orders/$f"
  echo "pg_dump failed"
  exit 1
fi
add_manifest "$BK/pg-orders" "$f"

# 2. SQLite, with and without a canary table.
python3 demo/seed/sqlite_backup.py "$SRC/notes.db" "$BK/sqlite-notes/notes-$TS.db" 1
add_manifest "$BK/sqlite-notes" "notes-$TS.db"
python3 demo/seed/sqlite_backup.py "$SRC/nocanary.db" "$BK/sqlite-nocanary/nocanary-$TS.db" 0
add_manifest "$BK/sqlite-nocanary" "nocanary-$TS.db"

# 3. Directory archive.
FILES="$SRC/files"
if [ ! -d "$FILES/data/docs" ]; then
  mkdir -p "$FILES/data/docs" "$FILES/config"
  for i in $(seq -w 1 20); do head -c 600 /dev/urandom | base64 > "$FILES/data/docs/doc-$i.txt"; done
  printf 'schema_version=3\napp=rdr-demo\n' > "$FILES/config/app.conf"
fi
date -u +%Y-%m-%dT%H:%M:%SZ > "$FILES/data/canary.txt"
f="files-$TS.tar.gz"
tar -C "$FILES" -czf "$BK/files-tar/$f" data config
add_manifest "$BK/files-tar" "$f"

echo "backups taken at $TS:"
for d in pg-orders sqlite-notes files-tar sqlite-nocanary; do
  ls -l "$BK/$d" | awk 'NR>1 && $NF != "SHA256SUMS" {print "  " $5 " bytes  " $NF}'
done
