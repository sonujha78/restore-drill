package cli

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sonujha78/restore-drill/internal/artifact"
)

func sqliteBytes() []byte {
	b := make([]byte, 4096)
	copy(b, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(b[16:], 4096)
	binary.BigEndian.PutUint32(b[24:], 1)
	binary.BigEndian.PutUint32(b[28:], 1)
	binary.BigEndian.PutUint32(b[92:], 1)
	return b
}

func cfgFor(t *testing.T, dir string) string {
	t.Helper()
	doc := fmt.Sprintf(`targets:
  - name: t1
    type: sqlite
    location: {dir: "%s", pattern: "t-*.db"}
    manifest: SHA256SUMS
    frequency: 1h
    retention: {min_copies: 1, max_copies: 3}
    rpo: 2h
    rto: 60s
    min_bytes: 4096
`, dir)
	return writeTemp(t, doc)
}

func putBackup(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func putManifest(t *testing.T, dir, name, hash string) {
	t.Helper()
	line := hash + "  " + name + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func goodBackup(t *testing.T, dir, name string) {
	t.Helper()
	p := putBackup(t, dir, name, sqliteBytes())
	h, err := artifact.SHA256File(p)
	if err != nil {
		t.Fatal(err)
	}
	putManifest(t, dir, name, h)
}

func TestArtifactPass(t *testing.T) {
	dir := t.TempDir()
	goodBackup(t, dir, "t-20260101.db")
	code, out, errOut := run("artifact", "--config", cfgFor(t, dir), "--target", "t1")
	if code != 0 || !strings.Contains(out, "overall: pass") || !strings.Contains(out, "t-20260101.db") {
		t.Fatalf("exit = %d, want 0 with 'overall: pass' (stdout %q, stderr %q)", code, out, errOut)
	}
}

func TestArtifactWithoutManifestIsUnverifiedExit1(t *testing.T) {
	dir := t.TempDir()
	putBackup(t, dir, "t-20260101.db", sqliteBytes())
	code, out, _ := run("artifact", "--config", cfgFor(t, dir), "--target", "t1")
	if code != 1 || !strings.Contains(out, "overall: unverified") {
		t.Fatalf("exit = %d, want 1 with 'overall: unverified' (stdout %q)", code, out)
	}
}

func TestArtifactWrongChecksumExit2(t *testing.T) {
	dir := t.TempDir()
	putBackup(t, dir, "t-20260101.db", sqliteBytes())
	putManifest(t, dir, "t-20260101.db", strings.Repeat("0", 64))
	code, out, _ := run("artifact", "--config", cfgFor(t, dir), "--target", "t1")
	if code != 2 || !strings.Contains(out, "differs") || !strings.Contains(out, "overall: fail") {
		t.Fatalf("exit = %d, want 2 with a checksum failure (stdout %q)", code, out)
	}
}

func TestArtifactNoFileExit2(t *testing.T) {
	code, out, _ := run("artifact", "--config", cfgFor(t, t.TempDir()), "--target", "t1")
	if code != 2 || !strings.Contains(out, "no file matches") {
		t.Fatalf("exit = %d, want 2 with 'no file matches' (stdout %q)", code, out)
	}
}

func TestArtifactPicksNewestByName(t *testing.T) {
	dir := t.TempDir()
	goodBackup(t, dir, "t-20260101.db")
	putBackup(t, dir, "t-20260202.db", nil) // newest name, but empty
	code, out, _ := run("artifact", "--config", cfgFor(t, dir), "--target", "t1")
	if code != 2 || !strings.Contains(out, "t-20260202.db") {
		t.Fatalf("exit = %d, want 2 on the newest (empty) file (stdout %q)", code, out)
	}
}

func TestArtifactFileFlagOverridesNewest(t *testing.T) {
	dir := t.TempDir()
	goodBackup(t, dir, "t-20260101.db")
	putBackup(t, dir, "t-20260202.db", nil)
	code, out, _ := run("artifact", "--config", cfgFor(t, dir), "--target", "t1",
		"--file", filepath.Join(dir, "t-20260101.db"))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 for the explicitly chosen good file (stdout %q)", code, out)
	}
}

func TestArtifactUnknownTargetAndMissingFlags(t *testing.T) {
	code, _, errOut := run("artifact", "--config", cfgFor(t, t.TempDir()), "--target", "nope")
	if code != 3 || !strings.Contains(errOut, "no target named") {
		t.Fatalf("exit = %d, stderr %q, want 3 and 'no target named'", code, errOut)
	}
	if code, _, _ := run("artifact"); code != 3 {
		t.Fatalf("exit = %d without flags, want 3", code)
	}
}

func TestValidateChecksTypesAgainstRegisteredAdapters(t *testing.T) {
	bad := strings.Replace(okYAML, "type: sqlite", "type: frobnicate", 1)
	code, _, errOut := run("validate", "--config", writeTemp(t, bad))
	if code != 3 || !strings.Contains(errOut, "unknown type; known types: postgres_dump, sqlite, tar_gz") {
		t.Fatalf("exit = %d, stderr %q, want 3 and the list of registered types", code, errOut)
	}
}
