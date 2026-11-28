package cli

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/sonujha78/restore-drill/internal/verify"
)

func verifyCfg(t *testing.T, withCanary bool) string {
	t.Helper()
	canary := ""
	if withCanary {
		canary = "    canary: {table: rdr_canary, column: written_at}\n"
	}
	return writeTemp(t, fmt.Sprintf(`targets:
  - name: s1
    type: sqlite
    location: {dir: /nonexistent, pattern: "n-*.db"}
    frequency: 1h
    retention: {min_copies: 1, max_copies: 3}
    rpo: 2h
    rto: 60s
%s    structure:
      tables:
        - {name: notes, columns: [id, title]}
    content:
      row_counts:
        - {table: notes, min: 2, max: 10}
`, canary))
}

// sandboxWithDB makes a real sandbox and puts a SQLite database in it the way restore does.
func sandboxWithDB(t *testing.T, noteRows int) string {
	t.Helper()
	code, out, errOut := run("sandbox", "create", "--base", t.TempDir())
	if code != 0 {
		t.Fatalf("sandbox create: exit %d (%s)", code, errOut)
	}
	dir := strings.TrimSpace(out)
	db, err := sql.Open("sqlite", filepath.Join(dir, "restored.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		"CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL)",
		"CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)",
		"INSERT INTO rdr_canary (written_at) VALUES ('2026-11-27T02:00:00Z')",
	}
	for i := 0; i < noteRows; i++ {
		stmts = append(stmts, fmt.Sprintf("INSERT INTO notes (title) VALUES ('t%d')", i))
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return dir
}

func TestVerifyHealthy(t *testing.T) {
	sb := sandboxWithDB(t, 5)
	code, out, errOut := run("verify", "--config", verifyCfg(t, true), "--target", "s1", "--sandbox", sb, "--now", "2026-11-27T02:47:30Z")
	if code != 0 || !strings.Contains(out, "overall: pass") {
		t.Fatalf("exit = %d, want 0 with overall: pass (stdout %q, stderr %q)", code, out, errOut)
	}
	if !strings.Contains(out, "47m30s") || !strings.Contains(out, "within") {
		t.Fatalf("stdout = %q, want the worked example RPO 47m30s and 'within'", out)
	}
}

func TestVerifyNoCanaryIsUnverifiedExit1(t *testing.T) {
	sb := sandboxWithDB(t, 5)
	code, out, _ := run("verify", "--config", verifyCfg(t, false), "--target", "s1", "--sandbox", sb)
	if code != 1 || !strings.Contains(out, "RPO cannot be measured") || !strings.Contains(out, "overall: unverified") {
		t.Fatalf("exit = %d, stdout %q, want 1 and the RPO-unknown explanation", code, out)
	}
}

func TestVerifyEmptyButValidIsExit2(t *testing.T) {
	sb := sandboxWithDB(t, 0)
	code, out, _ := run("verify", "--config", verifyCfg(t, true), "--target", "s1", "--sandbox", sb)
	if code != 2 || !strings.Contains(out, "overall: fail") {
		t.Fatalf("exit = %d, stdout %q, want 2 for a backup that restores but holds no data", code, out)
	}
}

func TestVerifyStaleBackupStillPassesChecks(t *testing.T) {
	sb := sandboxWithDB(t, 5)
	code, out, _ := run("verify", "--config", verifyCfg(t, true), "--target", "s1", "--sandbox", sb, "--now", "2026-11-27T07:00:00Z")
	if code != 0 || !strings.Contains(out, "5h0m0s") || !strings.Contains(out, "over") {
		t.Fatalf("exit = %d, stdout %q, want 0 and an RPO of 5h0m0s marked over", code, out)
	}
}

func TestVerifyRefusesANonSandbox(t *testing.T) {
	code, _, errOut := run("verify", "--config", verifyCfg(t, true), "--target", "s1", "--sandbox", t.TempDir())
	if code != 3 || !strings.Contains(errOut, "refusing") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a refusal", code, errOut)
	}
}

func TestVerifyPostgresNeedsPgdb(t *testing.T) {
	code, _, errOut := run("verify", "--config", pgCfg(t, "rdr-pg"), "--target", "pg1")
	if code != 3 || !strings.Contains(errOut, "--pgdb") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a hint about --pgdb", code, errOut)
	}
}

func TestVerifyPostgresRefusesAContainerThatIsNotRdr(t *testing.T) {
	code, _, errOut := run("verify", "--config", pgCfg(t, "other-db"), "--target", "pg1", "--pgdb", validPGName)
	if code != 3 || !strings.Contains(errOut, "refusing") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a refusal", code, errOut)
	}
}

func TestVerifyPostgresRefusesNonSandboxDatabaseNames(t *testing.T) {
	for _, name := range []string{"rdrdb", "postgres", "template1", "x; DROP DATABASE rdrdb"} {
		code, _, errOut := run("verify", "--config", pgCfg(t, "rdr-pg"), "--target", "pg1", "--pgdb", name)
		if code != 3 || !strings.Contains(errOut, "refusing") {
			t.Errorf("%q: exit = %d, stderr %q, want 3 and a refusal", name, code, errOut)
		}
	}
}

func TestVerifyUsageErrors(t *testing.T) {
	sb := sandboxWithDB(t, 5)
	cfg := verifyCfg(t, true)
	for _, args := range [][]string{
		{"verify"},
		{"verify", "--config", cfg},
		{"verify", "--config", cfg, "--target", "s1"},
		{"verify", "--config", cfg, "--target", "nope", "--sandbox", sb},
		{"verify", "--config", cfg, "--target", "s1", "--sandbox", sb, "--now", "yesterday"},
	} {
		if code, _, _ := run(args...); code != 3 {
			t.Errorf("run(%v): exit = %d, want 3", args, code)
		}
	}
}
