#!/usr/bin/env python3
"""Create the demo SQLite source if needed, write a canary row, take a backup copy.
usage: sqlite_backup.py SOURCE_DB BACKUP_FILE WITH_CANARY(0|1)
Synthetic data only."""
import base64
import os
import sqlite3
import sys
from datetime import datetime, timezone

src_path, dest_path, with_canary = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
now = datetime.now(timezone.utc).isoformat()

src = sqlite3.connect(src_path)
src.execute("CREATE TABLE IF NOT EXISTS notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL)")
if src.execute("SELECT count(*) FROM notes").fetchone()[0] == 0:
    rows = [(f"note {i}", base64.b64encode(os.urandom(90)).decode(), now) for i in range(1, 151)]
    src.executemany("INSERT INTO notes (title, body, created_at) VALUES (?, ?, ?)", rows)
if with_canary:
    src.execute("CREATE TABLE IF NOT EXISTS rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)")
    src.execute("INSERT INTO rdr_canary (written_at) VALUES (?)", (now,))
src.commit()

dst = sqlite3.connect(dest_path)
src.backup(dst)
dst.close()
src.close()
