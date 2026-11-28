package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
)

// drillDir makes a directory holding one real SQLite backup and its manifest.
func drillDir(t *testing.T, notes int) string {
	t.Helper()
	dir := t.TempDir()
	name := "notes-20261127T100000Z.db"
	db, err := sql.Open("sqlite", filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)",
		"CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)",
		"INSERT INTO rdr_canary (written_at) VALUES ('" + time.Now().UTC().Format(time.RFC3339Nano) + "')",
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
	h, err := artifact.SHA256File(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(h+"  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func drillCfgFor(t *testing.T, dir string, canary bool) string {
	t.Helper()
	c := ""
	if canary {
		c = "    canary: {table: rdr_canary, column: written_at}\n"
	}
	return writeTemp(t, fmt.Sprintf(`targets:
  - name: d1
    type: sqlite
    location: {dir: "%s", pattern: "notes-*.db"}
    manifest: SHA256SUMS
    frequency: 1h
    retention: {min_copies: 1, max_copies: 3}
    rpo: 2h
    rto: 1m
    min_bytes: 1
%s    structure:
      tables:
        - {name: notes, columns: [id, title]}
    content:
      row_counts:
        - {table: notes, min: 2, max: 10}
`, dir, c))
}

type drillFile struct {
	Targets []struct{ Name, Status string }
	Gate    struct {
		Exit     int
		Decision string
	}
}

func readDrill(t *testing.T, out string) drillFile {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(out, "drill-*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("result files in %s = %v, %v, want exactly one", out, files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var d drillFile
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	return d
}

func drillRun(t *testing.T, cfg string, extra ...string) (int, string, string, string) {
	t.Helper()
	out := filepath.Join(t.TempDir(), "out")
	args := append([]string{"drill", "--config", cfg, "--out", out, "--sandbox-base", filepath.Join(t.TempDir(), "sbx")}, extra...)
	code, so, se := run(args...)
	return code, so, se, out
}

func TestDrillCmdVerifiedBackupIsExit0AndRecorded(t *testing.T) {
	code, out, errOut, outDir := drillRun(t, drillCfgFor(t, drillDir(t, 5), true))
	if code != 0 || !strings.Contains(out, "verified") || !strings.Contains(out, "gate: ok") {
		t.Fatalf("exit = %d, stdout %q, stderr %q, want 0, 'verified' and 'gate: ok'", code, out, errOut)
	}
	d := readDrill(t, outDir)
	if len(d.Targets) != 1 || d.Targets[0].Status != "verified" || d.Gate.Exit != 0 {
		t.Fatalf("recorded result = %+v, want one verified target and gate 0", d)
	}
}

func TestDrillCmdNoCanaryIsUnverifiedExit1(t *testing.T) {
	code, out, _, outDir := drillRun(t, drillCfgFor(t, drillDir(t, 5), false))
	if code != 1 || !strings.Contains(out, "unverified") || !strings.Contains(out, "RPO cannot be measured") {
		t.Fatalf("exit = %d, stdout %q, want 1, 'unverified' and the RPO explanation", code, out)
	}
	if d := readDrill(t, outDir); d.Targets[0].Status != "unverified" || d.Gate.Decision != "degraded" {
		t.Fatalf("recorded = %+v, want unverified and degraded", d)
	}
}

func TestDrillCmdNoBackupIsFailedExit2(t *testing.T) {
	code, out, _, _ := drillRun(t, drillCfgFor(t, t.TempDir(), true))
	if code != 2 || !strings.Contains(out, "failed") || !strings.Contains(out, "no file matches") {
		t.Fatalf("exit = %d, stdout %q, want 2, 'failed' and 'no file matches'", code, out)
	}
}

func TestDrillCmdEmptyButValidBackupIsExit2(t *testing.T) {
	code, out, _, _ := drillRun(t, drillCfgFor(t, drillDir(t, 0), true))
	if code != 2 || !strings.Contains(out, "failed") {
		t.Fatalf("exit = %d, stdout %q, want 2 for a backup that restores but holds no data", code, out)
	}
}

func TestDrillCmdUsageErrors(t *testing.T) {
	cfg := drillCfgFor(t, drillDir(t, 5), true)
	for _, args := range [][]string{
		{"drill"},
		{"drill", "--config", cfg, "--target", "nope", "--out", filepath.Join(t.TempDir(), "o")},
		{"drill", "--config", filepath.Join(t.TempDir(), "missing.yaml")},
	} {
		if code, _, _ := run(args...); code != 3 {
			t.Errorf("run(%v): exit = %d, want 3", args, code)
		}
	}
}
