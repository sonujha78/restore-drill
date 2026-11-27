package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/clock"
)

// The on-disk marker format is pinned here on purpose: changing it must be a deliberate act.
func wantMarker(name string) string { return "restore-drill sandbox v1\nid=" + name + "\n" }

func fixedClock() clock.Clock {
	return clock.NewFake(time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
}

func newSandbox(t *testing.T) (*Sandbox, string) {
	t.Helper()
	base := t.TempDir()
	sb, err := Create(base, fixedClock())
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	return sb, base
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	return names
}

// ---------- Create ----------

func TestCreateMakesMarkedPrivateDirectory(t *testing.T) {
	sb, base := newSandbox(t)
	wantPrefix := DirPrefix + "20261127T100000Z-"
	if !strings.HasPrefix(sb.Name(), wantPrefix) {
		t.Fatalf("Name() = %q, want prefix %q", sb.Name(), wantPrefix)
	}
	st, err := os.Stat(sb.Root())
	if err != nil || !st.IsDir() {
		t.Fatalf("Root() %q is not a directory: %v", sb.Root(), err)
	}
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %o, want 700", st.Mode().Perm())
	}
	b, err := os.ReadFile(filepath.Join(sb.Root(), MarkerName))
	if err != nil || string(b) != wantMarker(sb.Name()) {
		t.Fatalf("marker = %q, %v, want %q", b, err, wantMarker(sb.Name()))
	}
	realBase, _ := filepath.EvalSymlinks(base)
	if filepath.Dir(sb.Root()) != realBase {
		t.Fatalf("sandbox lives in %q, want %q", filepath.Dir(sb.Root()), realBase)
	}
}

func TestCreateNamesAreUnique(t *testing.T) {
	base := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		sb, err := Create(base, fixedClock())
		if err != nil {
			t.Fatalf("Create #%d: %v", i, err)
		}
		if seen[sb.Name()] {
			t.Fatalf("name %q was handed out twice", sb.Name())
		}
		seen[sb.Name()] = true
	}
}

func TestCreateRefusesBadBase(t *testing.T) {
	if _, err := Create(filepath.Join(t.TempDir(), "missing"), fixedClock()); err == nil {
		t.Fatal("Create in a missing base directory succeeded")
	}
	f := filepath.Join(t.TempDir(), "file")
	mustWrite(t, f, "x")
	if _, err := Create(f, fixedClock()); err == nil {
		t.Fatal("Create with a file as base succeeded")
	}
}

// ---------- Open: refuse everything that is not a marked sandbox ----------

func TestOpenRefusesEverythingThatIsNotAMarkedSandbox(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T) string
	}{
		{"plain directory", func(t *testing.T) string { return t.TempDir() }},
		{"right name, no marker", func(t *testing.T) string {
			d := filepath.Join(t.TempDir(), DirPrefix+"nomarker")
			mustMkdir(t, d)
			return d
		}},
		{"right name, wrong marker text", func(t *testing.T) string {
			d := filepath.Join(t.TempDir(), DirPrefix+"wrongtext")
			mustMkdir(t, d)
			mustWrite(t, filepath.Join(d, MarkerName), "something else\n")
			return d
		}},
		{"marker text of another sandbox", func(t *testing.T) string {
			d := filepath.Join(t.TempDir(), DirPrefix+"b")
			mustMkdir(t, d)
			mustWrite(t, filepath.Join(d, MarkerName), wantMarker(DirPrefix+"a"))
			return d
		}},
		{"marker copied into a plain directory", func(t *testing.T) string {
			d := t.TempDir()
			mustWrite(t, filepath.Join(d, MarkerName), wantMarker(filepath.Base(d)))
			return d
		}},
		{"real sandbox that was renamed", func(t *testing.T) string {
			sb, base := newSandbox(t)
			renamed := filepath.Join(base, DirPrefix+"renamed")
			if err := os.Rename(sb.Root(), renamed); err != nil {
				t.Fatal(err)
			}
			return renamed
		}},
		{"marker is a symlink", func(t *testing.T) string {
			root := t.TempDir()
			d := filepath.Join(root, DirPrefix+"linked")
			mustMkdir(t, d)
			target := filepath.Join(root, "elsewhere")
			mustWrite(t, target, wantMarker(DirPrefix+"linked"))
			if err := os.Symlink(target, filepath.Join(d, MarkerName)); err != nil {
				t.Fatal(err)
			}
			return d
		}},
		{"a regular file", func(t *testing.T) string {
			f := filepath.Join(t.TempDir(), DirPrefix+"file")
			mustWrite(t, f, wantMarker(DirPrefix+"file"))
			return f
		}},
		{"does not exist", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), DirPrefix+"missing")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb, err := Open(c.build(t))
			if !errors.Is(err, ErrNotSandbox) {
				t.Fatalf("Open() = %v, %v, want ErrNotSandbox", sb, err)
			}
		})
	}
}

func TestOpenAcceptsASandboxMadeByCreate(t *testing.T) {
	sb, _ := newSandbox(t)
	again, err := Open(sb.Root())
	if err != nil {
		t.Fatalf("Open(real sandbox) error: %v", err)
	}
	if again.Name() != sb.Name() || again.Root() != sb.Root() {
		t.Fatalf("Open() = %s %s, want %s %s", again.Name(), again.Root(), sb.Name(), sb.Root())
	}
}

func TestZeroValueAndNilSandboxesAreRefused(t *testing.T) {
	var zero Sandbox
	var nilSB *Sandbox
	for name, s := range map[string]*Sandbox{"zero value": &zero, "nil": nilSB} {
		if err := s.Verify(); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: Verify() = %v, want ErrNotSandbox", name, err)
		}
		if _, err := s.CreateFile("a.txt", 0o600); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: CreateFile() error = %v, want ErrNotSandbox", name, err)
		}
		if err := s.MkdirAll("d"); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: MkdirAll() = %v, want ErrNotSandbox", name, err)
		}
		if err := s.Remove(); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: Remove() = %v, want ErrNotSandbox", name, err)
		}
	}
}

// ---------- Resolve and CreateFile: nothing leaves the sandbox ----------

func TestResolveRejectsEscapes(t *testing.T) {
	sb, _ := newSandbox(t)
	bad := []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd", MarkerName, "sub/../" + MarkerName, "a\x00b"}
	for _, rel := range bad {
		got, err := sb.Resolve(rel)
		if !errors.Is(err, ErrEscape) {
			t.Errorf("Resolve(%q) = %q, %v, want ErrEscape", rel, got, err)
		}
	}
}

func TestResolveAcceptsPathsInside(t *testing.T) {
	sb, _ := newSandbox(t)
	for rel, want := range map[string]string{"a/b/c.txt": "a/b/c.txt", "a/../b": "b", "./x": "x", "d/": "d"} {
		got, err := sb.Resolve(rel)
		w := filepath.Join(sb.Root(), filepath.FromSlash(want))
		if err != nil || got != w {
			t.Errorf("Resolve(%q) = %q, %v, want %q", rel, got, err, w)
		}
	}
}

func TestCreateFileWritesInsideAndNeverOverwrites(t *testing.T) {
	sb, _ := newSandbox(t)
	f, err := sb.CreateFile("d/e/f.txt", 0o644)
	if err != nil {
		t.Fatalf("CreateFile() error: %v", err)
	}
	if _, err := f.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(sb.Root(), "d", "e", "f.txt"))
	if err != nil || string(b) != "hello" {
		t.Fatalf("file content = %q, %v, want hello", b, err)
	}
	if f2, err := sb.CreateFile("d/e/f.txt", 0o644); err == nil {
		f2.Close()
		t.Fatal("second CreateFile on the same path succeeded, an existing file must never be overwritten")
	}
}

func TestCreateFileStripsGroupAndOtherBits(t *testing.T) {
	sb, _ := newSandbox(t)
	cases := []struct {
		name     string
		in, want os.FileMode
	}{{"x.sh", 0o777, 0o700}, {"y.txt", 0o644, 0o600}, {"z.txt", 0o400, 0o600}}
	for _, c := range cases {
		f, err := sb.CreateFile(c.name, c.in)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		st, err := os.Stat(filepath.Join(sb.Root(), c.name))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != c.want {
			t.Errorf("%s: mode = %o, want %o", c.name, st.Mode().Perm(), c.want)
		}
	}
}

// Deliberate attempt: a symlink inside the sandbox points at a directory outside it.
func TestSymlinksInsideTheSandboxAreNeverFollowed(t *testing.T) {
	sb, _ := newSandbox(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(sb.Root(), "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.CreateFile("link/pwned.txt", 0o600); !errors.Is(err, ErrEscape) {
		t.Fatalf("CreateFile through a directory symlink: err = %v, want ErrEscape", err)
	}
	if err := sb.MkdirAll("link/sub"); !errors.Is(err, ErrEscape) {
		t.Fatalf("MkdirAll through a directory symlink: err = %v, want ErrEscape", err)
	}
	if names := listDir(t, outside); len(names) != 0 {
		t.Fatalf("something was written outside the sandbox: %v", names)
	}

	target := filepath.Join(outside, "target.txt")
	if err := os.Symlink(target, filepath.Join(sb.Root(), "flink")); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.CreateFile("flink", 0o600); !errors.Is(err, ErrEscape) {
		t.Fatalf("CreateFile on a file symlink: err = %v, want ErrEscape", err)
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the symlink target was created: %v", err)
	}
}

// ---------- Remove ----------

func TestRemoveDeletesOnlyAVerifiedSandbox(t *testing.T) {
	sb, _ := newSandbox(t)
	f, err := sb.CreateFile("x.txt", 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err := sb.Remove(); err != nil {
		t.Fatalf("Remove() error: %v", err)
	}
	if _, err := os.Stat(sb.Root()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("sandbox directory still exists: %v", err)
	}
	if err := sb.Verify(); !errors.Is(err, ErrNotSandbox) {
		t.Fatalf("Verify() after Remove = %v, want ErrNotSandbox", err)
	}
}

func TestRemoveRefusesWhenTheMarkerIsGone(t *testing.T) {
	sb, _ := newSandbox(t)
	if err := os.Remove(filepath.Join(sb.Root(), MarkerName)); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(sb.Root(), "keep.txt")
	mustWrite(t, keep, "precious")
	if err := sb.Remove(); !errors.Is(err, ErrNotSandbox) {
		t.Fatalf("Remove() = %v, want ErrNotSandbox", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a directory without a marker was deleted anyway: %v", err)
	}
}

// ---------- Real directories ----------

// Open only reads. It must say no to every real directory below, including the
// real demo backups when they exist on this machine.
func TestRealDirectoriesAreNotSandboxes(t *testing.T) {
	dirs := []string{".", "../..", os.TempDir(), "/"}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, home)
	}
	for _, d := range []string{"../../demo/backups", "../../demo/source"} {
		if _, err := os.Stat(d); err == nil {
			dirs = append(dirs, d)
		}
	}
	for _, d := range dirs {
		if sb, err := Open(d); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("Open(%q) = %v, %v, want ErrNotSandbox", d, sb, err)
		}
	}
}
