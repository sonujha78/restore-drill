package verify

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/config"
)

func makeDB(t *testing.T, stmts ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	return p
}

func openSrc(t *testing.T, p string) *SQLiteSource {
	t.Helper()
	s, err := OpenSQLite(p)
	if err != nil {
		t.Fatalf("OpenSQLite: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const notesSchema = "CREATE TABLE notes (id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL)"

func notesDB(t *testing.T) *SQLiteSource {
	return openSrc(t, makeDB(t, notesSchema,
		"INSERT INTO notes (title, body) VALUES ('a','x'),('b','y'),('c','z')"))
}

func TestDriverWorks(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow("SELECT sqlite_version()").Scan(&v); err != nil || v == "" {
		t.Fatalf("sqlite_version() = %q, %v", v, err)
	}
}

func TestOpenIsReadOnlyAndNeverCreatesAFile(t *testing.T) {
	s := notesDB(t)
	if _, err := s.db.Exec("CREATE TABLE x (a)"); err == nil {
		t.Fatal("a write on the verify connection succeeded, it must be read-only")
	}
	missing := filepath.Join(t.TempDir(), "nope.db")
	if s2, err := OpenSQLite(missing); err == nil {
		s2.Close()
		t.Fatal("OpenSQLite on a missing file succeeded")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("OpenSQLite created a file: %v", err)
	}
}

func TestTablesAndColumns(t *testing.T) {
	s := openSrc(t, makeDB(t, notesSchema, "CREATE TABLE other (k TEXT)"))
	got, err := s.Tables()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || strings.Join(got["notes"], ",") != "id,title,body" || strings.Join(got["other"], ",") != "k" {
		t.Fatalf("Tables() = %v, want notes(id,title,body) and other(k)", got)
	}
}

func TestRowCountAndIdentifierCheck(t *testing.T) {
	s := notesDB(t)
	if n, err := s.RowCount("notes"); err != nil || n != 3 {
		t.Fatalf("RowCount = %d, %v, want 3", n, err)
	}
	if _, err := s.RowCount("notes; DROP TABLE notes"); err == nil {
		t.Fatal("RowCount accepted an invalid identifier")
	}
	if _, err := s.RowCount("missing"); err == nil {
		t.Fatal("RowCount on a missing table returned no error")
	}
}

func TestCanaryReturnsTheNewestValue(t *testing.T) {
	s := openSrc(t, makeDB(t,
		"CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)",
		"INSERT INTO rdr_canary (written_at) VALUES ('2026-11-27T01:00:00.123456+00:00'),('2026-11-27T05:43:37.250000+00:00'),('2026-11-26T23:00:00+00:00')"))
	got, err := s.Canary(config.Canary{Table: "rdr_canary", Column: "written_at"})
	want := time.Date(2026, 11, 27, 5, 43, 37, 250000000, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Fatalf("Canary = %v, %v, want %v", got, err, want)
	}
}

func TestSQLiteCanaryProblems(t *testing.T) {
	empty := openSrc(t, makeDB(t, "CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)"))
	if _, err := empty.Canary(config.Canary{Table: "rdr_canary", Column: "written_at"}); !errors.Is(err, ErrNoCanaryData) {
		t.Fatalf("empty canary table: err = %v, want ErrNoCanaryData", err)
	}
	bad := openSrc(t, makeDB(t,
		"CREATE TABLE rdr_canary (id INTEGER PRIMARY KEY, written_at TEXT NOT NULL)",
		"INSERT INTO rdr_canary (written_at) VALUES ('hunter2')"))
	_, err := bad.Canary(config.Canary{Table: "rdr_canary", Column: "written_at"})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("bad canary value: err = %v, want an error that does not echo the value", err)
	}
	if _, err := bad.Canary(config.Canary{Path: "x"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("path canary on a database: err = %v, want ErrUnsupported", err)
	}
}

func TestSampleHash(t *testing.T) {
	s := notesDB(t)
	h1, n1, err := s.SampleHash("notes", 2)
	h2, n2, _ := s.SampleHash("notes", 2)
	h3, n3, _ := s.SampleHash("notes", 3)
	if err != nil || n1 != 2 || n2 != 2 || n3 != 3 {
		t.Fatalf("rows sampled = %d/%d/%d, err %v, want 2/2/3", n1, n2, n3, err)
	}
	if h1 != h2 || h1 == h3 || len(h1) != 64 {
		t.Fatalf("hashes %q %q %q: want same input -> same 64-char hash, different input -> different hash", h1, h2, h3)
	}
	other := openSrc(t, makeDB(t, notesSchema, "INSERT INTO notes (title, body) VALUES ('a','CHANGED')"))
	if ho, _, _ := other.SampleHash("notes", 2); ho == h1 {
		t.Fatal("a changed row did not change the hash")
	}
}

func TestScalarAndIntegrity(t *testing.T) {
	s := notesDB(t)
	if v, err := s.Scalar("SELECT count(*) FROM notes"); err != nil || v != "3" {
		t.Fatalf("Scalar = %q, %v, want 3", v, err)
	}
	if v, err := s.Integrity(); err != nil || v != "ok" {
		t.Fatalf("Integrity = %q, %v, want ok", v, err)
	}
	if _, err := s.PathExists("x"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PathExists on a database: err = %v, want ErrUnsupported", err)
	}
}

// A brutally damaged page must not look healthy. (A single flipped bit may or
// may not be caught by integrity_check; that is measured on real data, not assumed here.)
func TestIntegrityNoticesAWreckedPage(t *testing.T) {
	stmts := []string{notesSchema}
	for i := 0; i < 300; i++ {
		stmts = append(stmts, "INSERT INTO notes (title, body) VALUES ('title','"+strings.Repeat("b", 200)+"')")
	}
	p := makeDB(t, stmts...)
	b, err := os.ReadFile(p)
	if err != nil || len(b) < 4*4096 {
		t.Fatalf("test database too small: %d bytes, %v", len(b), err)
	}
	for i := 4096; i < 3*4096; i++ {
		b[i] = 0xFF
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := OpenSQLite(p)
	if err != nil {
		return // refusing to open at all is also not "healthy"
	}
	defer s.Close()
	if v, err := s.Integrity(); err == nil && v == "ok" {
		t.Fatal("integrity_check said ok on a database with wrecked pages")
	}
}
