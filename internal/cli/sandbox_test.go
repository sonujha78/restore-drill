package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSandboxCreateCheckRemove(t *testing.T) {
	base := t.TempDir()
	code, out, errOut := run("sandbox", "create", "--base", base)
	if code != 0 {
		t.Fatalf("create: exit = %d, want 0 (stderr %q)", code, errOut)
	}
	dir := strings.TrimSpace(out)
	if !strings.HasPrefix(filepath.Base(dir), "rdr-sbx-") {
		t.Fatalf("create printed %q, want the path of an rdr-sbx- directory", dir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("printed path is not a directory: %v", err)
	}
	code, out, errOut = run("sandbox", "check", "--dir", dir)
	if code != 0 || !strings.Contains(out, "OK") {
		t.Fatalf("check: exit = %d, stdout %q, stderr %q, want 0 and OK", code, out, errOut)
	}
	code, _, errOut = run("sandbox", "remove", "--dir", dir)
	if code != 0 {
		t.Fatalf("remove: exit = %d, want 0 (stderr %q)", code, errOut)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("sandbox still exists after remove: %v", err)
	}
}

func TestSandboxCheckRefusesAPlainDirectory(t *testing.T) {
	code, out, errOut := run("sandbox", "check", "--dir", t.TempDir())
	if code != 3 || !strings.Contains(errOut, "refusing") || !strings.Contains(errOut, "not a restore-drill sandbox") {
		t.Fatalf("exit = %d, stderr %q, want 3 and a refusal (stdout %q)", code, errOut, out)
	}
}

func TestSandboxRemoveRefusesNonSandboxesAndKeepsTheirFiles(t *testing.T) {
	plain := t.TempDir()
	decoy := filepath.Join(t.TempDir(), "rdr-sbx-decoy")
	if err := os.Mkdir(decoy, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{plain, decoy} {
		keep := filepath.Join(d, "keep.txt")
		if err := os.WriteFile(keep, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		code, _, errOut := run("sandbox", "remove", "--dir", d)
		if code != 3 || !strings.Contains(errOut, "refusing") {
			t.Fatalf("remove %s: exit = %d, stderr %q, want 3 and a refusal", filepath.Base(d), code, errOut)
		}
		if _, err := os.Stat(keep); err != nil {
			t.Fatalf("remove %s deleted a file anyway: %v", filepath.Base(d), err)
		}
	}
}

func TestSandboxUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{"sandbox"},
		{"sandbox", "frobnicate"},
		{"sandbox", "create"},
		{"sandbox", "check"},
		{"sandbox", "remove"},
	} {
		if code, _, _ := run(args...); code != 3 {
			t.Errorf("run(%v): exit = %d, want 3", args, code)
		}
	}
}
