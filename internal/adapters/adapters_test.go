package adapters

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/drill"
	"github.com/sonujha78/restore-drill/internal/exitcode"
)

func dur(d time.Duration) config.Duration { return config.Duration{Duration: d} }

func writeManifest(t *testing.T, dir, name string) {
	t.Helper()
	h, err := artifact.SHA256File(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(h+"  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeSQLite(t *testing.T, dir, name string, notes int, canary time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)",
		"CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)",
		"INSERT INTO rdr_canary (written_at) VALUES ('" + canary.UTC().Format(time.RFC3339Nano) + "')",
	}
	for i := 0; i < notes; i++ {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO notes (title) VALUES ('t%d')", i))
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			db.Close()
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

type tarFile struct{ name, body string }

func makeTarGz(t *testing.T, path string, files []tarFile) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := gzip.NewWriter(f)
	tw := tar.NewWriter(zw)
	for _, e := range files {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func sqliteTarget(dir string) config.Target {
	return config.Target{
		Name: "sq", Type: "sqlite",
		Location: config.Location{Dir: dir, Pattern: "notes-*.db"},
		Manifest: "SHA256SUMS", RPO: dur(2 * time.Hour), RTO: dur(time.Minute), MinBytes: 1,
		Canary:    &config.Canary{Table: "rdr_canary", Column: "written_at"},
		Structure: config.Structure{Tables: []config.TableSpec{{Name: "notes", Columns: []string{"id", "title"}}}},
		Content:   config.Content{RowCounts: []config.RowCount{{Table: "notes", Min: 1, Max: 100}}},
		AppChecks: []config.AppCheck{{Name: "no-empty-titles", Query: "SELECT count(*) FROM notes WHERE title = ''", Expect: "0"}},
	}
}

func tarTarget(dir string) config.Target {
	return config.Target{
		Name: "tr", Type: "tar_gz",
		Location: config.Location{Dir: dir, Pattern: "files-*.tar.gz"},
		Manifest: "SHA256SUMS", RPO: dur(2 * time.Hour), RTO: dur(time.Minute), MinBytes: 1,
		Canary:    &config.Canary{Path: "data/canary.txt"},
		Structure: config.Structure{Paths: []string{"data/canary.txt", "config/app.conf"}},
		AppChecks: []config.AppCheck{{Name: "has-schema-version", Command: "grep -c schema_version config/app.conf", Expect: "1"}},
	}
}

func runDrill(t *testing.T, tg config.Target, base string) *drill.Result {
	t.Helper()
	res, err := drill.Run(&config.Config{Targets: []config.Target{tg}},
		drill.Options{Clock: clock.Real{}, SandboxBase: filepath.Join(base, "sbx")})
	if err != nil {
		t.Fatalf("drill.Run error: %v", err)
	}
	if len(res.Targets) != 1 {
		t.Fatalf("len(Targets) = %d, want 1", len(res.Targets))
	}
	return res
}

func sandboxesLeft(t *testing.T, base string) int {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(base, "sbx"))
	if err != nil {
		return 0
	}
	return len(es)
}

func TestAdaptersCoverEveryArtifactKind(t *testing.T) {
	got, want := strings.Join(drill.Kinds(), ","), strings.Join(artifact.Kinds(), ",")
	if got != want || got != "postgres_dump,sqlite,tar_gz" {
		t.Fatalf("drill kinds = %q, artifact kinds = %q, want both postgres_dump,sqlite,tar_gz", got, want)
	}
}

func TestAdaptersSQLiteBackupIsDrilledEndToEnd(t *testing.T) {
	dir, base := t.TempDir(), t.TempDir()
	makeSQLite(t, dir, "notes-20261127T100000Z.db", 5, time.Now().Add(-10*time.Minute))
	writeManifest(t, dir, "notes-20261127T100000Z.db")
	res := runDrill(t, sqliteTarget(dir), base)
	tr := res.Targets[0]
	if tr.Status != drill.StatusVerified || res.Gate.Exit != exitcode.OK {
		t.Fatalf("Status %q, gate %d, reasons %v, want verified and 0", tr.Status, res.Gate.Exit, tr.Reasons)
	}
	if n := sandboxesLeft(t, base); n != 0 {
		t.Fatalf("%d sandbox directories left behind", n)
	}
}

func TestAdaptersEmptySQLiteBackupFails(t *testing.T) {
	dir, base := t.TempDir(), t.TempDir()
	makeSQLite(t, dir, "notes-20261127T100000Z.db", 0, time.Now())
	if err := os.Remove(filepath.Join(dir, "notes-20261127T100000Z.db")); err != nil {
		t.Fatal(err)
	}
	// schema only: restores fine, holds no notes and no canary row
	db, err := sql.Open("sqlite", filepath.Join(dir, "notes-20261127T100000Z.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)",
		"CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)",
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	writeManifest(t, dir, "notes-20261127T100000Z.db")
	tr := runDrill(t, sqliteTarget(dir), base).Targets[0]
	if tr.Status != drill.StatusFailed {
		t.Fatalf("Status = %q, want failed for a backup that restores but holds no data (%v)", tr.Status, tr.Reasons)
	}
}

func TestAdaptersTarBackupIsDrilledEndToEnd(t *testing.T) {
	dir, base := t.TempDir(), t.TempDir()
	name := "files-20261127T100000Z.tar.gz"
	makeTarGz(t, filepath.Join(dir, name), []tarFile{
		{"data/canary.txt", time.Now().Add(-5*time.Minute).UTC().Format(time.RFC3339) + "\n"},
		{"config/app.conf", "schema_version=3\n"},
		{"data/docs/a.txt", "alpha\n"},
	})
	writeManifest(t, dir, name)
	res := runDrill(t, tarTarget(dir), base)
	tr := res.Targets[0]
	if tr.Status != drill.StatusVerified || res.Gate.Exit != exitcode.OK {
		t.Fatalf("Status %q, gate %d, reasons %v, want verified and 0", tr.Status, res.Gate.Exit, tr.Reasons)
	}
	if n := sandboxesLeft(t, base); n != 0 {
		t.Fatalf("%d sandbox directories left behind", n)
	}
}

func TestAdaptersTarEscapeAttemptFailsAndWritesNothingOutside(t *testing.T) {
	dir, base := t.TempDir(), t.TempDir()
	name := "files-20261127T100000Z.tar.gz"
	makeTarGz(t, filepath.Join(dir, name), []tarFile{{"../escaped.txt", "x"}})
	writeManifest(t, dir, name)
	tr := runDrill(t, tarTarget(dir), base).Targets[0]
	if tr.Status != drill.StatusFailed || !strings.Contains(strings.Join(tr.Reasons, " "), "escapes") {
		t.Fatalf("Status %q, reasons %v, want failed naming the escape", tr.Status, tr.Reasons)
	}
	if _, err := os.Stat(filepath.Join(base, "sbx", "escaped.txt")); err == nil {
		t.Fatal("a file escaped the sandbox")
	}
	if n := sandboxesLeft(t, base); n != 0 {
		t.Fatalf("%d sandbox directories left behind", n)
	}
}

func TestAdaptersPostgresRefusesAContainerThatIsNotRdr(t *testing.T) {
	dir, base := t.TempDir(), t.TempDir()
	name := "orders-20261127T100000Z.dump"
	data := append([]byte("PGDMP\x01\x0f\x00\x04\x08\x01"), make([]byte, 3000)...)
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeManifest(t, dir, name)
	tg := config.Target{
		Name: "pg", Type: "postgres_dump",
		Location: config.Location{Dir: dir, Pattern: "orders-*.dump"},
		Manifest: "SHA256SUMS", RPO: dur(2 * time.Hour), RTO: dur(time.Minute), MinBytes: 1,
		Options: map[string]string{"container": "postgres-other"},
	}
	res := runDrill(t, tg, base)
	tr := res.Targets[0]
	if tr.Status != drill.StatusUnverified || !strings.Contains(strings.Join(tr.Reasons, " "), "refusing") {
		t.Fatalf("Status %q, reasons %v, want unverified and 'refusing'", tr.Status, tr.Reasons)
	}
	if res.Gate.Exit != exitcode.Degraded {
		t.Fatalf("gate = %d, want 1", res.Gate.Exit)
	}
}
