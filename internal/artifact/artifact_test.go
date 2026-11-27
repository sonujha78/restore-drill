package artifact

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	hashABC   = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" // sha256("abc")
	hashEmpty = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")
)

func write(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func find(t *testing.T, rs []Result, check string) Result {
	t.Helper()
	for _, r := range rs {
		if r.Check == check {
			return r
		}
	}
	t.Fatalf("no result named %q in %+v", check, rs)
	return Result{}
}

func expect(t *testing.T, r Result, s Status, contains string) {
	t.Helper()
	if r.Status != s {
		t.Fatalf("%s: status = %v (detail %q), want %v", r.Check, r.Status, r.Detail, s)
	}
	if !strings.Contains(r.Detail, contains) {
		t.Fatalf("%s: detail = %q, want it to contain %q", r.Check, r.Detail, contains)
	}
}

// ---------- synthetic artifacts ----------

func sqliteDB(pages int) []byte {
	b := make([]byte, 4096*pages)
	copy(b, "SQLite format 3\x00")
	binary.BigEndian.PutUint16(b[16:], 4096)
	binary.BigEndian.PutUint32(b[24:], 7)
	binary.BigEndian.PutUint32(b[28:], uint32(pages))
	binary.BigEndian.PutUint32(b[92:], 7)
	return b
}

func pgHeader() []byte {
	b := make([]byte, 64)
	copy(b, "PGDMP\x01\x0f\x00\x04\x08\x01")
	return b
}

func makeTarGz(t *testing.T, entries int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < entries; i++ {
		data := make([]byte, 2000)
		rng.Read(data)
		hdr := &tar.Header{Name: fmt.Sprintf("data/f%d.bin", i), Mode: 0o600, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
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

// ---------- status ----------

func TestZeroStatusIsUnverified(t *testing.T) {
	var s Status
	if s != Unverified {
		t.Fatalf("zero Status = %v, want Unverified (a forgotten status must never look like a pass)", s)
	}
	if Pass.String() != "pass" || Fail.String() != "fail" || Unverified.String() != "unverified" {
		t.Fatalf("String() = %q/%q/%q", Pass.String(), Fail.String(), Unverified.String())
	}
}

func TestOverall(t *testing.T) {
	p, u, f := Result{Status: Pass}, Result{Status: Unverified}, Result{Status: Fail}
	cases := []struct {
		name string
		in   []Result
		want Status
	}{
		{"no results is never a pass", nil, Unverified},
		{"all pass", []Result{p, p}, Pass},
		{"one unverified", []Result{p, u, p}, Unverified},
		{"fail beats unverified", []Result{p, u, f}, Fail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Overall(c.in); got != c.want {
				t.Fatalf("Overall = %v, want %v", got, c.want)
			}
		})
	}
}

// ---------- checksum ----------

func TestSHA256WorkedExamples(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name string
		data []byte
		want string
	}{{"abc", []byte("abc"), hashABC}, {"empty", nil, hashEmpty}} {
		got, err := SHA256File(write(t, dir, c.name, c.data))
		if err != nil || got != c.want {
			t.Fatalf("SHA256File(%s) = %q, %v, want %q", c.name, got, err, c.want)
		}
	}
}

func TestParseManifest(t *testing.T) {
	doc := "# comment\n\n" + hashABC + "  a.db\n" + strings.ToUpper(hashEmpty) + " *b.tar.gz\n"
	m, err := ParseManifest(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("ParseManifest error: %v", err)
	}
	if m["a.db"] != hashABC || m["b.tar.gz"] != hashEmpty || len(m) != 2 {
		t.Fatalf("manifest = %v, want a.db and b.tar.gz with lower-case hashes", m)
	}
}

func TestParseManifestRejectsBadLines(t *testing.T) {
	cases := []struct{ name, doc, want string }{
		{"not hex", "zzzz  a.db\n", "line 1"},
		{"short hash", "abc  a.db\n", "line 1"},
		{"no name", hashABC + "\n", "line 1"},
		{"second line bad", hashABC + "  a.db\nnonsense\n", "line 2"},
		{"conflicting duplicate", hashABC + "  a.db\n" + hashEmpty + "  a.db\n", "twice"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseManifest(strings.NewReader(c.doc))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestCheckChecksum(t *testing.T) {
	dir := t.TempDir()
	f := write(t, dir, "a.db", []byte("abc"))
	zeros := strings.Repeat("0", 64)
	cases := []struct {
		name     string
		manifest string // file content, "" = no file, "-" = no manifest configured
		want     Status
		detail   string
	}{
		{"match", hashABC + "  a.db\n", Pass, "matches"},
		{"mismatch", zeros + "  a.db\n", Fail, "differs"},
		{"not listed", hashABC + "  other.db\n", Unverified, "not listed"},
		{"malformed manifest", "garbage\n", Unverified, "line 1"},
		{"manifest missing", "", Unverified, "not found"},
		{"no manifest configured", "-", Unverified, "no manifest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mp := filepath.Join(t.TempDir(), "SHA256SUMS")
			switch c.manifest {
			case "-":
				mp = ""
			case "":
			default:
				mp = write(t, filepath.Dir(mp), "SHA256SUMS", []byte(c.manifest))
			}
			expect(t, CheckChecksum(f, mp), c.want, c.detail)
		})
	}
}

// ---------- exists and size ----------

func TestCheckExists(t *testing.T) {
	dir := t.TempDir()
	expect(t, CheckExists(write(t, dir, "x", []byte("x"))), Pass, "regular")
	expect(t, CheckExists(filepath.Join(dir, "nope")), Fail, "does not exist")
	expect(t, CheckExists(dir), Fail, "not a regular file")
}

func TestCheckSize(t *testing.T) {
	cases := []struct {
		name      string
		size, min int64
		want      Status
		detail    string
	}{
		{"empty file", 0, 1024, Fail, "empty"},
		{"empty even when min is 0", 0, 0, Fail, "empty"},
		{"below min", 1023, 1024, Fail, "below"},
		{"exactly min", 1024, 1024, Pass, "1024"},
		{"min not set", 5, 0, Pass, "5 bytes"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			expect(t, CheckSize(c.size, c.min), c.want, c.detail)
		})
	}
}

// ---------- tar.gz ----------

func TestTarGzValid(t *testing.T) {
	p := write(t, t.TempDir(), "a.tar.gz", makeTarGz(t, 5))
	expect(t, find(t, readTarGz(p), "readable"), Pass, "5 entries")
}

func TestTarGzFaults(t *testing.T) {
	good := makeTarGz(t, 5)
	flipped := append([]byte(nil), good...)
	flipped[len(flipped)/2] ^= 0x01
	cases := []struct {
		name   string
		data   []byte
		detail string
	}{
		{"truncated in half", good[:len(good)/2], ""},
		{"only the gzip trailer cut off", good[:len(good)-8], ""},
		{"single bit flip in the middle", flipped, ""},
		{"not gzip at all", []byte("this is not gzip at all"), "gzip"},
		{"empty file", nil, ""},
		{"valid archive with no entries", makeTarGz(t, 0), "no entries"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := write(t, t.TempDir(), "a.tar.gz", c.data)
			expect(t, find(t, readTarGz(p), "readable"), Fail, c.detail)
		})
	}
}

// ---------- sqlite ----------

func TestSQLiteValid(t *testing.T) {
	rs := readSQLite(write(t, t.TempDir(), "a.db", sqliteDB(3)))
	expect(t, find(t, rs, "header"), Pass, "page size 4096")
	expect(t, find(t, rs, "readable"), Pass, "3 pages")
}

func TestSQLiteFaults(t *testing.T) {
	db := sqliteDB(3)
	stale := sqliteDB(3)
	binary.BigEndian.PutUint32(stale[92:], 6) // version-valid-for != change counter
	noCount := sqliteDB(3)
	binary.BigEndian.PutUint32(noCount[28:], 0)
	badPage := sqliteDB(3)
	binary.BigEndian.PutUint16(badPage[16:], 1000)
	badMagic := sqliteDB(3)
	copy(badMagic, "NotSQLite fmt 3\x00")

	cases := []struct {
		name   string
		data   []byte
		check  string
		want   Status
		detail string
	}{
		{"header page missing", db[:50], "header", Fail, "smaller than"},
		{"bad magic", badMagic, "header", Fail, "magic"},
		{"invalid page size", badPage, "header", Fail, "page size"},
		{"last page cut off", db[:2*4096], "readable", Fail, "header says 3"},
		{"cut in the middle of a page", db[:2*4096+2048], "readable", Fail, "multiple"},
		{"stale change counter", stale, "readable", Unverified, "not trustworthy"},
		{"header has no page count", noCount, "readable", Unverified, "not trustworthy"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := readSQLite(write(t, t.TempDir(), "a.db", c.data))
			expect(t, find(t, rs, c.check), c.want, c.detail)
		})
	}
}

// ---------- pg_dump custom format ----------

func TestPGDump(t *testing.T) {
	rs := readPGDump(write(t, t.TempDir(), "a.dump", pgHeader()))
	expect(t, find(t, rs, "header"), Pass, "custom-format")
	expect(t, find(t, rs, "readable"), Unverified, "pg_restore")

	tarFmt := pgHeader()
	tarFmt[10] = 3
	cases := []struct {
		name   string
		data   []byte
		detail string
	}{
		{"bad magic", append([]byte("NOTPG"), make([]byte, 20)...), "magic"},
		{"tar format, not custom", tarFmt, "custom format"},
		{"shorter than header", []byte("PGDMP"), "shorter"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rs := readPGDump(write(t, t.TempDir(), "a.dump", c.data))
			expect(t, find(t, rs, "header"), Fail, c.detail)
		})
	}
}

// ---------- Run ----------

func TestRegisteredKinds(t *testing.T) {
	if got := strings.Join(Kinds(), ","); got != "postgres_dump,sqlite,tar_gz" {
		t.Fatalf("Kinds() = %q, want postgres_dump,sqlite,tar_gz", got)
	}
}

func TestRunHealthySQLite(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "t.db", sqliteDB(1))
	h, _ := SHA256File(p)
	m := write(t, dir, "SHA256SUMS", []byte(h+"  t.db\n"))
	rs := Run(Params{Kind: "sqlite", Path: p, ManifestPath: m, MinBytes: 4096})
	if Overall(rs) != Pass {
		t.Fatalf("Overall = %v for a healthy backup: %+v", Overall(rs), rs)
	}
}

func TestRunMissingFileStopsEarly(t *testing.T) {
	rs := Run(Params{Kind: "sqlite", Path: filepath.Join(t.TempDir(), "nope.db")})
	if len(rs) != 1 || rs[0].Check != "exists" || rs[0].Status != Fail {
		t.Fatalf("results = %+v, want exactly one failing exists result", rs)
	}
}

func TestRunUnknownKindIsNeverAPass(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "t.db", sqliteDB(1))
	h, _ := SHA256File(p)
	m := write(t, dir, "SHA256SUMS", []byte(h+"  t.db\n"))
	rs := Run(Params{Kind: "no-such-type", Path: p, ManifestPath: m})
	expect(t, find(t, rs, "readable"), Unverified, "no reader registered")
	if Overall(rs) != Unverified {
		t.Fatalf("Overall = %v, want Unverified", Overall(rs))
	}
}

func TestRunTruncatedSQLiteWithManifestFailsTwice(t *testing.T) {
	dir := t.TempDir()
	orig := sqliteDB(3)
	h, _ := SHA256File(write(t, dir, "orig.db", orig))
	p := write(t, dir, "t.db", orig[:2*4096])
	m := write(t, dir, "SHA256SUMS", []byte(h+"  t.db\n"))
	rs := Run(Params{Kind: "sqlite", Path: p, ManifestPath: m})
	expect(t, find(t, rs, "checksum"), Fail, "differs")
	expect(t, find(t, rs, "readable"), Fail, "header says 3")
	if Overall(rs) != Fail {
		t.Fatalf("Overall = %v, want Fail", Overall(rs))
	}
}

// A bit flip inside a SQLite data page leaves header and page count intact.
// Only the checksum can catch it. Without a manifest the result must be
// unverified, never pass.
func TestSQLiteBitFlipWithoutManifestIsUnverified(t *testing.T) {
	data := sqliteDB(3)
	data[2*4096+100] ^= 0x01
	p := write(t, t.TempDir(), "t.db", data)
	rs := Run(Params{Kind: "sqlite", Path: p})
	expect(t, find(t, rs, "readable"), Pass, "3 pages")
	expect(t, find(t, rs, "checksum"), Unverified, "no manifest")
	if Overall(rs) != Unverified {
		t.Fatalf("Overall = %v, want Unverified", Overall(rs))
	}
}
