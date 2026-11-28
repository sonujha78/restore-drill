package verify

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/config"
)

func fileRoot(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	for p, c := range map[string]string{
		"data/canary.txt": "2026-11-27T02:00:00Z\n",
		"config/app.conf": "schema_version=3\napp=demo\n",
		"data/docs/a.txt": "alpha\n",
	} {
		full := filepath.Join(d, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestFilePathExists(t *testing.T) {
	f := NewFileSource(fileRoot(t))
	for p, want := range map[string]bool{"data/canary.txt": true, "data/docs": true, "data/nope.txt": false} {
		if got, err := f.PathExists(p); err != nil || got != want {
			t.Errorf("PathExists(%q) = %v, %v, want %v", p, got, err, want)
		}
	}
	for _, bad := range []string{"../x", "/etc/passwd", "a/../../x", ""} {
		if _, err := f.PathExists(bad); err == nil {
			t.Errorf("PathExists(%q) accepted a path that leaves the root", bad)
		}
	}
}

func TestFileCanary(t *testing.T) {
	root := fileRoot(t)
	f := NewFileSource(root)
	got, err := f.Canary(config.Canary{Path: "data/canary.txt"})
	if err != nil || !got.Equal(time.Date(2026, 11, 27, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("Canary = %v, %v, want 02:00:00", got, err)
	}
	if _, err := f.Canary(config.Canary{Path: "data/missing.txt"}); !errors.Is(err, ErrNoCanaryData) {
		t.Fatalf("missing canary file: err = %v, want ErrNoCanaryData", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data", "canary.txt"), []byte("hunter2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Canary(config.Canary{Path: "data/canary.txt"}); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("garbage canary: err = %v, want an error that does not echo the content", err)
	}
	if _, err := f.Canary(config.Canary{Path: "../x"}); err == nil {
		t.Fatal("canary path that leaves the root was accepted")
	}
	if _, err := f.Canary(config.Canary{Table: "t", Column: "c"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("table canary on a file tree: err = %v, want ErrUnsupported", err)
	}
}

func TestFileCommands(t *testing.T) {
	f := NewFileSource(fileRoot(t))
	if v, err := f.Command("grep -c schema_version config/app.conf"); err != nil || v != "1" {
		t.Fatalf("Command = %q, %v, want 1", v, err)
	}
	// grep exits 1 when nothing matches but still prints 0: that is an answer, not a failure.
	if v, err := f.Command("grep -c nothere config/app.conf"); err != nil || v != "0" {
		t.Fatalf("Command = %q, %v, want 0 without an error", v, err)
	}
	for _, bad := range []string{
		"rm -rf data", "sh -c id", "", "   ",
		"cat /etc/passwd", "cat ../x", "grep -c x config/../../x",
	} {
		if v, err := f.Command(bad); err == nil {
			t.Errorf("Command(%q) = %q, want it refused", bad, v)
		}
	}
	if _, err := f.Command("grep -c x data/docs/a.txt"); err != nil {
		t.Errorf("an allowed command inside the root was refused: %v", err)
	}
}

func TestFileSourceHasNoDatabaseAbilities(t *testing.T) {
	f := NewFileSource(fileRoot(t))
	if _, err := f.Tables(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Tables: %v, want ErrUnsupported", err)
	}
	if _, err := f.RowCount("t"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("RowCount: %v, want ErrUnsupported", err)
	}
	if _, _, err := f.SampleHash("t", 1); !errors.Is(err, ErrUnsupported) {
		t.Errorf("SampleHash: %v, want ErrUnsupported", err)
	}
	if _, err := f.Scalar("SELECT 1"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Scalar: %v, want ErrUnsupported", err)
	}
	if _, err := f.Integrity(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Integrity: %v, want ErrUnsupported", err)
	}
}
