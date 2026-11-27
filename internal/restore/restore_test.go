package restore

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/sandbox"
)

type entry struct {
	hdr  tar.Header
	data string
}

func buildTar(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := e.hdr
		if h.Typeflag == tar.TypeReg || h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
			h.Size = int64(len(e.data))
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			if _, err := tw.Write([]byte(e.data)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func reg(name, data string) entry { return entry{hdr: tar.Header{Name: name}, data: data} }

func newSB(t *testing.T) (*sandbox.Sandbox, string) {
	t.Helper()
	base := t.TempDir()
	sb, err := sandbox.Create(base, clock.NewFake(time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	return sb, base
}

func writeSrc(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "src.tar.gz")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestTarRestoresFilesIntoTheSandbox(t *testing.T) {
	sb, _ := newSB(t)
	src := writeSrc(t, buildTar(t, []entry{
		{hdr: tar.Header{Name: "data/", Typeflag: tar.TypeDir, Mode: 0o755}},
		reg("data/a.txt", "alpha"),
		reg("config/app.conf", "schema_version=3\n"),
	}))
	res, err := restoreTarGz(src, sb, DefaultLimits())
	if err != nil {
		t.Fatalf("restoreTarGz error: %v", err)
	}
	if res.Files != 2 || res.Bytes != int64(len("alpha")+len("schema_version=3\n")) {
		t.Fatalf("result = %+v, want 2 files and 22 bytes", res)
	}
	b, err := os.ReadFile(filepath.Join(sb.Root(), "data", "a.txt"))
	if err != nil || string(b) != "alpha" {
		t.Fatalf("data/a.txt = %q, %v, want alpha", b, err)
	}
}

// Deliberate escape attempts. Each must fail and write nothing outside the sandbox.
func TestTarEscapeAttemptsFailAndWriteNothingOutside(t *testing.T) {
	cases := []struct {
		name    string
		entries func(outside string) []entry
	}{
		{"dot-dot entry", func(o string) []entry { return []entry{reg("../evil.txt", "x")} }},
		{"deep dot-dot entry", func(o string) []entry { return []entry{reg("a/../../evil.txt", "x")} }},
		{"absolute path entry", func(o string) []entry { return []entry{reg(filepath.Join(o, "abs.txt"), "x")} }},
		{"symlink then write through it", func(o string) []entry {
			return []entry{
				{hdr: tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: o}},
				reg("link/pwned.txt", "x"),
			}
		}},
		{"hardlink entry", func(o string) []entry {
			return []entry{
				reg("a.txt", "x"),
				{hdr: tar.Header{Name: "b.txt", Typeflag: tar.TypeLink, Linkname: "a.txt"}},
			}
		}},
		{"marker overwrite", func(o string) []entry { return []entry{reg(sandbox.MarkerName, "tampered")} }},
		{"fifo entry", func(o string) []entry {
			return []entry{{hdr: tar.Header{Name: "pipe", Typeflag: tar.TypeFifo}}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sb, base := newSB(t)
			outside := t.TempDir()
			src := writeSrc(t, buildTar(t, c.entries(outside)))
			if _, err := restoreTarGz(src, sb, DefaultLimits()); err == nil {
				t.Fatal("restore succeeded, want an error")
			}
			if got := names(t, outside); len(got) != 0 {
				t.Fatalf("files appeared outside the sandbox: %v", got)
			}
			if got := names(t, base); len(got) != 1 {
				t.Fatalf("base directory now holds %v, want only the sandbox", got)
			}
			if err := sb.Verify(); err != nil {
				t.Fatalf("sandbox marker damaged: %v", err)
			}
		})
	}
}

func TestTarLimitsStopABomb(t *testing.T) {
	sb, _ := newSB(t)
	big := string(make([]byte, 5000))
	src := writeSrc(t, buildTar(t, []entry{reg("a", big), reg("b", big)}))
	if _, err := restoreTarGz(src, sb, Limits{MaxBytes: 6000, MaxEntries: 100}); err == nil {
		t.Fatal("restore past MaxBytes succeeded")
	}
	sb2, _ := newSB(t)
	src2 := writeSrc(t, buildTar(t, []entry{reg("a", "1"), reg("b", "2"), reg("c", "3")}))
	if _, err := restoreTarGz(src2, sb2, Limits{MaxBytes: 1 << 20, MaxEntries: 2}); err == nil {
		t.Fatal("restore past MaxEntries succeeded")
	}
}

func TestTarCorruptedArchivesAreErrors(t *testing.T) {
	good := buildTar(t, []entry{reg("a.txt", string(make([]byte, 3000))), reg("b.txt", "bbb")})
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)/2] ^= 0x01
	for name, data := range map[string][]byte{
		"truncated": good[:len(good)/2],
		"bit flip":  flipped,
		"not gzip":  []byte("plain text"),
	} {
		sb, _ := newSB(t)
		if _, err := restoreTarGz(writeSrc(t, data), sb, DefaultLimits()); err == nil {
			t.Errorf("%s: restore succeeded, want an error", name)
		}
	}
}

func TestSQLiteRestoreCopiesTheFile(t *testing.T) {
	sb, _ := newSB(t)
	src := filepath.Join(t.TempDir(), "notes-1.db")
	content := bytes.Repeat([]byte("sqlite-bytes"), 400)
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := restoreSQLite(src, sb, DefaultLimits())
	if err != nil {
		t.Fatalf("restoreSQLite error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(sb.Root(), "restored.db"))
	if err != nil || !bytes.Equal(b, content) {
		t.Fatalf("restored.db differs from the source (%v)", err)
	}
	if res.Files != 1 || res.Bytes != int64(len(content)) {
		t.Fatalf("result = %+v, want 1 file and %d bytes", res, len(content))
	}
}

func TestRestoreRefusesANonSandbox(t *testing.T) {
	var zero sandbox.Sandbox
	src := writeSrc(t, buildTar(t, []entry{reg("a.txt", "x")}))
	for _, kind := range []string{"tar_gz", "sqlite"} {
		_, err := Run(kind, src, &zero, DefaultLimits(), clock.Real{})
		if !errors.Is(err, sandbox.ErrNotSandbox) {
			t.Errorf("%s: Run on a zero sandbox = %v, want ErrNotSandbox", kind, err)
		}
	}
	if _, err := Run("tar_gz", src, nil, DefaultLimits(), clock.Real{}); !errors.Is(err, sandbox.ErrNotSandbox) {
		t.Errorf("Run on nil sandbox = %v, want ErrNotSandbox", err)
	}
}

func TestRunMeasuresRTOWithTheInjectedClock(t *testing.T) {
	sb, _ := newSB(t)
	clk := clock.NewFake(time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
	Register("test_slow", func(src string, sb *sandbox.Sandbox, lim Limits) (Result, error) {
		clk.Advance(400 * time.Second)
		return Result{Files: 1}, nil
	})
	res, err := Run("test_slow", "unused", sb, DefaultLimits(), clk)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if res.Duration != 400*time.Second {
		t.Fatalf("Duration = %v, want 400s", res.Duration)
	}
}

func TestRunUnknownKind(t *testing.T) {
	sb, _ := newSB(t)
	if _, err := Run("no-such-type", "x", sb, DefaultLimits(), clock.Real{}); err == nil {
		t.Fatal("Run with an unknown kind succeeded")
	}
}

func TestRegisteredKinds(t *testing.T) {
	got := Kinds()
	has := map[string]bool{}
	for _, k := range got {
		has[k] = true
	}
	if !has["tar_gz"] || !has["sqlite"] {
		t.Fatalf("Kinds() = %v, want tar_gz and sqlite", got)
	}
}
