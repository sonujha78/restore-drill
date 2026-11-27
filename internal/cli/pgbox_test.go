package cli

import (
	"fmt"
	"strings"
	"testing"
)

func pgCfg(t *testing.T, container string) string {
	t.Helper()
	return writeTemp(t, fmt.Sprintf(`targets:
  - name: pg1
    type: postgres_dump
    location: {dir: /nonexistent, pattern: "o-*.dump"}
    frequency: 1h
    retention: {min_copies: 1, max_copies: 3}
    rpo: 2h
    rto: 60s
    options:
      container: %s
`, container))
}

const validPGName = "rdr_sbx_20261127t100000z_aaaaaaaa"

func TestPGSandboxUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"pgsandbox"},
		{"pgsandbox", "frobnicate"},
		{"pgsandbox", "create"},
		{"pgsandbox", "check", "--container", "rdr-pg"},
		{"pgsandbox", "remove", "--container", "rdr-pg"},
	} {
		if code, _, _ := run(args...); code != 3 {
			t.Errorf("run(%v): exit = %d, want 3", args, code)
		}
	}
}

func TestPGSandboxRefusesAContainerThatIsNotRdr(t *testing.T) {
	for _, sub := range []string{"create", "check", "remove"} {
		args := []string{"pgsandbox", sub, "--container", "postgres-other", "--db", validPGName}
		code, _, errOut := run(args...)
		if code != 3 || !strings.Contains(errOut, "refusing") {
			t.Errorf("%s: exit = %d, stderr %q, want 3 and a refusal", sub, code, errOut)
		}
	}
}

func TestPGSandboxRefusesNamesThatAreNotSandboxDatabases(t *testing.T) {
	for _, name := range []string{"rdrdb", "postgres", "template1", "x; DROP DATABASE rdrdb"} {
		for _, sub := range []string{"check", "remove"} {
			code, _, errOut := run("pgsandbox", sub, "--container", "rdr-pg", "--db", name)
			if code != 3 || !strings.Contains(errOut, "refusing") {
				t.Errorf("%s %q: exit = %d, stderr %q, want 3 and a refusal", sub, name, code, errOut)
			}
		}
	}
}

func TestRestorePGNeedsTheDatabaseFlag(t *testing.T) {
	code, _, errOut := run("restore", "--config", pgCfg(t, "rdr-pg"), "--target", "pg1")
	if code != 3 || !strings.Contains(errOut, "--pgdb") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a hint about --pgdb", code, errOut)
	}
}

func TestRestorePGRefusesAContainerThatIsNotRdr(t *testing.T) {
	code, _, errOut := run("restore", "--config", pgCfg(t, "other-db"), "--target", "pg1", "--pgdb", validPGName)
	if code != 3 || !strings.Contains(errOut, "refusing") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a refusal", code, errOut)
	}
}

func TestRestorePGRefusesNonSandboxDatabaseNames(t *testing.T) {
	for _, name := range []string{"rdrdb", "postgres", "template1", "x; DROP DATABASE rdrdb"} {
		code, _, errOut := run("restore", "--config", pgCfg(t, "rdr-pg"), "--target", "pg1", "--pgdb", name)
		if code != 3 || !strings.Contains(errOut, "refusing") {
			t.Errorf("%q: exit = %d, stderr %q, want 3 and a refusal", name, code, errOut)
		}
	}
}
