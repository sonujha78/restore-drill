package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func restoreCfg(t *testing.T, dir string) string {
	t.Helper()
	return writeTemp(t, fmt.Sprintf(`targets:
  - name: t1
    type: sqlite
    location: {dir: "%s", pattern: "t-*.db"}
    manifest: SHA256SUMS
    frequency: 1h
    retention: {min_copies: 1, max_copies: 3}
    rpo: 2h
    rto: 60s
`, dir))
}

func TestRestoreIntoSandbox(t *testing.T) {
	dir := t.TempDir()
	goodBackup(t, dir, "t-20260101.db")
	base := t.TempDir()
	_, sbOut, _ := run("sandbox", "create", "--base", base)
	sbDir := strings.TrimSpace(sbOut)
	code, out, errOut := run("restore", "--config", restoreCfg(t, dir), "--target", "t1", "--sandbox", sbDir)
	if code != 0 || !strings.Contains(out, "restored 1 files") || !strings.Contains(out, "restore time") {
		t.Fatalf("exit = %d, stdout %q, stderr %q, want 0 with a restore summary", code, out, errOut)
	}
	if _, err := os.Stat(filepath.Join(sbDir, "restored.db")); err != nil {
		t.Fatalf("restored.db missing: %v", err)
	}
}

func TestRestoreRefusesANonSandbox(t *testing.T) {
	dir := t.TempDir()
	goodBackup(t, dir, "t-20260101.db")
	plain := t.TempDir()
	code, _, errOut := run("restore", "--config", restoreCfg(t, dir), "--target", "t1", "--sandbox", plain)
	if code != 3 || !strings.Contains(errOut, "refusing") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a refusal", code, errOut)
	}
	if es, _ := os.ReadDir(plain); len(es) != 0 {
		t.Fatalf("something was written into a non-sandbox: %d entries", len(es))
	}
}

func TestRestoreNoFileIsExit2(t *testing.T) {
	_, sbOut, _ := run("sandbox", "create", "--base", t.TempDir())
	code, _, _ := run("restore", "--config", restoreCfg(t, t.TempDir()), "--target", "t1", "--sandbox", strings.TrimSpace(sbOut))
	if code != 2 {
		t.Fatalf("exit = %d, want 2 when there is no backup to restore", code)
	}
}

func TestRestoreUsageErrors(t *testing.T) {
	if code, _, _ := run("restore"); code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if code, _, _ := run("restore", "--config", "x"); code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
}
