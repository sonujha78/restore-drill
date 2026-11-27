package pgbox

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/clock"
)

// ---------- fake runner: records every command, emulates the few psql statements we use ----------

type call struct {
	args  []string
	stdin string
}

func (c call) joined() string { return strings.Join(c.args, " ") }

func dbArg(c call) string {
	for i, a := range c.args {
		if a == "-d" && i+1 < len(c.args) {
			return c.args[i+1]
		}
	}
	return ""
}

type fakeRunner struct {
	calls       []call
	dbs         map[string]string // database name -> comment
	failComment bool
	restoreErr  error
}

func newFake() *fakeRunner { return &fakeRunner{dbs: map[string]string{}} }

var (
	reCreate  = regexp.MustCompile(`^CREATE DATABASE (\S+)$`)
	reComment = regexp.MustCompile(`^COMMENT ON DATABASE (\S+) IS '(.*)'$`)
	reMarker  = regexp.MustCompile(`datname='([^']*)'`)
	reDrop    = regexp.MustCompile(`^DROP DATABASE (?:IF EXISTS )?(\S+)`)
)

func (f *fakeRunner) Run(stdin io.Reader, args ...string) (string, error) {
	c := call{args: append([]string(nil), args...)}
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		c.stdin = string(b)
	}
	f.calls = append(f.calls, c)
	switch args[0] {
	case "pg_restore":
		return "", f.restoreErr
	case "psql":
		sql := args[len(args)-1]
		switch {
		case reCreate.MatchString(sql):
			f.dbs[reCreate.FindStringSubmatch(sql)[1]] = ""
		case reComment.MatchString(sql):
			if f.failComment {
				return "", errors.New("comment failed")
			}
			m := reComment.FindStringSubmatch(sql)
			f.dbs[m[1]] = m[2]
		case strings.HasPrefix(sql, "SELECT shobj_description"):
			if m := reMarker.FindStringSubmatch(sql); m != nil {
				return f.dbs[m[1]], nil
			}
			return "", nil
		case reDrop.MatchString(sql):
			delete(f.dbs, reDrop.FindStringSubmatch(sql)[1])
		default:
			return "42", nil
		}
	}
	return "", nil
}

func (f *fakeRunner) has(substr string) bool {
	for _, c := range f.calls {
		if strings.Contains(c.joined(), substr) {
			return true
		}
	}
	return false
}

func fixed() clock.Clock {
	return clock.NewFake(time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC))
}

func mustCreate(t *testing.T, f *fakeRunner) *DB {
	t.Helper()
	db, err := Create(f, fixed())
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	return db
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "x.dump")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// ---------- Create ----------

func TestCreateMakesAMarkedDatabaseThroughTheAdminDatabaseOnly(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	if !strings.HasPrefix(db.Name(), "rdr_sbx_20261127t100000z_") || !nameRe.MatchString(db.Name()) {
		t.Fatalf("Name() = %q, want rdr_sbx_20261127t100000z_<8 hex>", db.Name())
	}
	if got, want := f.dbs[db.Name()], commentMagic+db.Name(); got != want {
		t.Fatalf("marker comment = %q, want %q", got, want)
	}
	for _, c := range f.calls {
		if d := dbArg(c); d != adminDB {
			t.Fatalf("Create touched database %q (%s), want only %q", d, c.joined(), adminDB)
		}
	}
}

func TestCreateNamesAreUnique(t *testing.T) {
	f := newFake()
	seen := map[string]bool{}
	for i := 0; i < 25; i++ {
		n := mustCreate(t, f).Name()
		if seen[n] {
			t.Fatalf("name %q handed out twice", n)
		}
		seen[n] = true
	}
}

func TestCreateCleansUpWhenTheMarkerCannotBeWritten(t *testing.T) {
	f := newFake()
	f.failComment = true
	if _, err := Create(f, fixed()); err == nil {
		t.Fatal("Create succeeded although the marker could not be written")
	}
	if len(f.dbs) != 0 {
		t.Fatalf("an unmarked database was left behind: %v", f.dbs)
	}
}

// ---------- Open: refuse everything that is not a marked sandbox database ----------

func TestOpenRefusesNamesThatCannotBeSandboxesWithoutTouchingPostgres(t *testing.T) {
	bad := []string{
		"rdrdb", "postgres", "template0", "template1", "", "rdr_sbx_", "rdr_sbx_x",
		"RDR_SBX_20261127T100000Z_AAAAAAAA",
		"rdr_sbx_20261127t100000z_aaaaaaaa ",
		"rdr_sbx_20261127t100000z_aaaaaaaa'; DROP DATABASE rdrdb; --",
		"rdr_sbx_20261127t100000z_aaaaaaaa; DROP DATABASE rdrdb",
	}
	for _, name := range bad {
		f := newFake()
		db, err := Open(f, name)
		if !errors.Is(err, ErrNotSandbox) {
			t.Errorf("Open(%q) = %v, %v, want ErrNotSandbox", name, db, err)
		}
		if len(f.calls) != 0 {
			t.Errorf("Open(%q) ran %d commands, want none for a bad name", name, len(f.calls))
		}
	}
	if _, err := Open(nil, "rdr_sbx_20261127t100000z_aaaaaaaa"); !errors.Is(err, ErrNotSandbox) {
		t.Errorf("Open with no runner = %v, want ErrNotSandbox", err)
	}
}

func TestOpenRefusesValidLookingNamesThatAreNotSandboxes(t *testing.T) {
	const name = "rdr_sbx_20261127t100000z_aaaaaaaa"
	cases := map[string]func(f *fakeRunner){
		"does not exist":            func(f *fakeRunner) {},
		"exists, no marker comment": func(f *fakeRunner) { f.dbs[name] = "" },
		"marker of another id":      func(f *fakeRunner) { f.dbs[name] = commentMagic + "rdr_sbx_20261127t100000z_bbbbbbbb" },
		"plain comment":             func(f *fakeRunner) { f.dbs[name] = "hello" },
	}
	for label, setup := range cases {
		t.Run(label, func(t *testing.T) {
			f := newFake()
			setup(f)
			if db, err := Open(f, name); !errors.Is(err, ErrNotSandbox) {
				t.Fatalf("Open() = %v, %v, want ErrNotSandbox", db, err)
			}
			if f.has("DROP") || f.has("pg_restore") {
				t.Fatalf("a refused Open ran a destructive command: %+v", f.calls)
			}
		})
	}
}

func TestOpenAcceptsADatabaseMadeByCreate(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	again, err := Open(f, db.Name())
	if err != nil || again.Name() != db.Name() {
		t.Fatalf("Open(real sandbox) = %v, %v", again, err)
	}
}

func TestZeroValueAndNilAreRefused(t *testing.T) {
	var zero DB
	var nilDB *DB
	for label, d := range map[string]*DB{"zero": &zero, "nil": nilDB} {
		if err := d.Verify(); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: Verify() = %v, want ErrNotSandbox", label, err)
		}
		if _, err := d.Restore("x", 1); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: Restore() = %v, want ErrNotSandbox", label, err)
		}
		if _, err := d.Scalar("SELECT 1"); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: Scalar() = %v, want ErrNotSandbox", label, err)
		}
		if err := d.Drop(); !errors.Is(err, ErrNotSandbox) {
			t.Errorf("%s: Drop() = %v, want ErrNotSandbox", label, err)
		}
	}
}

// ---------- Restore ----------

func TestRestoreSendsTheDumpToTheSandboxDatabaseOnly(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	src := writeFile(t, "PGDMP-bytes")
	f.calls = nil
	n, err := db.Restore(src, 1<<20)
	if err != nil || n != 11 {
		t.Fatalf("Restore() = %d, %v, want 11 bytes and no error", n, err)
	}
	var restores []call
	for _, c := range f.calls {
		if c.args[0] == "pg_restore" {
			restores = append(restores, c)
		}
		if d := dbArg(c); d != db.Name() && d != adminDB {
			t.Fatalf("a command targeted database %q: %s", d, c.joined())
		}
	}
	if len(restores) != 1 {
		t.Fatalf("pg_restore ran %d times, want 1", len(restores))
	}
	r := restores[0]
	if dbArg(r) != db.Name() || !strings.Contains(r.joined(), "--exit-on-error") || r.stdin != "PGDMP-bytes" {
		t.Fatalf("pg_restore call = %q stdin %q, want -d %s, --exit-on-error and the file as stdin", r.joined(), r.stdin, db.Name())
	}
}

func TestRestoreErrorsAreReturned(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	f.restoreErr = errors.New("exit status 1: pg_restore: error: unexpected end of file")
	if _, err := db.Restore(writeFile(t, "x"), 1<<20); err == nil || !strings.Contains(err.Error(), "pg_restore failed") {
		t.Fatalf("Restore() error = %v, want it to mention 'pg_restore failed'", err)
	}
}

func TestRestoreVerifiesTheMarkerFirst(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	f.dbs[db.Name()] = "" // marker removed behind our back
	if _, err := db.Restore(writeFile(t, "x"), 1<<20); !errors.Is(err, ErrNotSandbox) {
		t.Fatalf("Restore() = %v, want ErrNotSandbox", err)
	}
	if f.has("pg_restore") {
		t.Fatal("pg_restore ran although the marker was gone")
	}
}

func TestRestoreRefusesAFileAboveTheLimit(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	if _, err := db.Restore(writeFile(t, "0123456789"), 5); err == nil {
		t.Fatal("Restore of a file above maxBytes succeeded")
	}
	if f.has("pg_restore") {
		t.Fatal("pg_restore ran for a file above the limit")
	}
}

// ---------- Drop and Scalar ----------

func TestDropRemovesOnlyAVerifiedSandbox(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	if err := db.Drop(); err != nil {
		t.Fatalf("Drop() error: %v", err)
	}
	if _, ok := f.dbs[db.Name()]; ok {
		t.Fatal("database still exists after Drop")
	}

	f2 := newFake()
	db2 := mustCreate(t, f2)
	f2.dbs[db2.Name()] = ""
	if err := db2.Drop(); !errors.Is(err, ErrNotSandbox) {
		t.Fatalf("Drop() without marker = %v, want ErrNotSandbox", err)
	}
	if _, ok := f2.dbs[db2.Name()]; !ok || f2.has("DROP") {
		t.Fatal("a database without a marker was dropped anyway")
	}
}

func TestScalarRunsInsideTheSandboxDatabase(t *testing.T) {
	f := newFake()
	db := mustCreate(t, f)
	f.calls = nil
	got, err := db.Scalar("SELECT count(*) FROM orders")
	if err != nil || got != "42" {
		t.Fatalf("Scalar() = %q, %v, want 42", got, err)
	}
	last := f.calls[len(f.calls)-1]
	if dbArg(last) != db.Name() {
		t.Fatalf("Scalar ran in %q, want %q", dbArg(last), db.Name())
	}
}

// ---------- Docker runner: only rdr- containers ----------

func TestDockerRunnerOnlyAcceptsRdrContainers(t *testing.T) {
	for _, ok := range []string{"rdr-pg", "rdr-pg-2", "rdr-a.b_c"} {
		if _, err := NewDockerRunner(ok); err != nil {
			t.Errorf("NewDockerRunner(%q) = %v, want accepted", ok, err)
		}
	}
	for _, bad := range []string{"", "rdr-", "postgres-other", "other-db", "RDR-pg", "-rdr-pg", "rdr-pg; rm -rf /", "rdr-pg extra", "xrdr-pg"} {
		if _, err := NewDockerRunner(bad); err == nil {
			t.Errorf("NewDockerRunner(%q) accepted, want refused", bad)
		}
	}
}

// ---------- SafeError: no row content ----------

func TestSafeErrorKeepsOneErrorLineAndRedactsQuotedValues(t *testing.T) {
	in := "pg_restore: error: COPY failed for table \"customers\": ERROR:  value \"hunter2\" bad\n" +
		"CONTEXT:  COPY customers, line 3: \"customer hunter2\"\n"
	got := SafeError(in)
	if strings.Contains(got, "hunter2") || strings.Contains(got, "customers") || !strings.HasPrefix(got, "pg_restore: error:") {
		t.Fatalf("SafeError() = %q, want one error line with quoted text redacted", got)
	}
}

func TestSafeErrorWithOnlyContextLinesRevealsNothing(t *testing.T) {
	got := SafeError("CONTEXT:  COPY customers, line 3: \"customer hunter2\"\nDETAIL:  Key (id)=(5) already exists.\n")
	if strings.Contains(got, "hunter2") || strings.Contains(got, "(5)") {
		t.Fatalf("SafeError() = %q leaked content", got)
	}
}

func TestSafeErrorIsShort(t *testing.T) {
	if got := SafeError("ERROR:  " + strings.Repeat("a", 500)); len(got) > 200 {
		t.Fatalf("len(SafeError()) = %d, want <= 200", len(got))
	}
}
