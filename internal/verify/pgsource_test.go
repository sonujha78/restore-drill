package verify

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/measure"
)

type qfake struct {
	calls  []string
	answer func(sql string) (string, error)
}

func (f *qfake) Scalar(sql string) (string, error) {
	f.calls = append(f.calls, sql)
	return f.answer(sql)
}

func answers(out string, err error) *qfake {
	return &qfake{answer: func(string) (string, error) { return out, err }}
}

var hash64 = strings.Repeat("ab", 32)

func TestPGTables(t *testing.T) {
	f := answers("orders:id\norders:amount\ncustomers:id\n", nil)
	got, err := NewPGSource(f).Tables()
	if err != nil || len(got) != 2 || strings.Join(got["orders"], ",") != "id,amount" || strings.Join(got["customers"], ",") != "id" {
		t.Fatalf("Tables() = %v, %v, want orders(id,amount) and customers(id)", got, err)
	}
	if len(f.calls) != 1 || !strings.Contains(f.calls[0], "table_schema = 'public'") {
		t.Fatalf("queries = %v, want one query restricted to the public schema", f.calls)
	}
	if got, err := NewPGSource(answers("", nil)).Tables(); err != nil || len(got) != 0 {
		t.Fatalf("empty output: Tables() = %v, %v, want an empty map and no error", got, err)
	}
	if _, err := NewPGSource(answers("garbage-without-separator", nil)).Tables(); err == nil {
		t.Fatal("malformed output was accepted")
	}
	if _, err := NewPGSource(answers("", errors.New("boom"))).Tables(); err == nil {
		t.Fatal("a query error was swallowed")
	}
}

func TestPGRowCount(t *testing.T) {
	f := answers("42", nil)
	n, err := NewPGSource(f).RowCount("orders")
	if err != nil || n != 42 || !strings.Contains(f.calls[0], `"orders"`) {
		t.Fatalf("RowCount = %d, %v, queries %v, want 42 and a quoted table name", n, err, f.calls)
	}
	if _, err := NewPGSource(answers("hunter2", nil)).RowCount("orders"); err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("non-numeric output: err = %v, want an error that does not echo it", err)
	}
	f = answers("1", nil)
	if _, err := NewPGSource(f).RowCount("orders; DROP TABLE orders"); err == nil || len(f.calls) != 0 {
		t.Fatalf("invalid identifier: err = %v, queries %v, want an error and no query at all", err, f.calls)
	}
	if _, err := NewPGSource(answers("", errors.New("boom"))).RowCount("orders"); err == nil {
		t.Fatal("a query error was swallowed")
	}
}

func TestPGSampleHash(t *testing.T) {
	f := answers("7 "+hash64, nil)
	h, n, err := NewPGSource(f).SampleHash("orders", 7)
	if err != nil || h != hash64 || n != 7 {
		t.Fatalf("SampleHash = %q, %d, %v, want the hash and 7 rows", h, n, err)
	}
	if !strings.Contains(f.calls[0], "LIMIT 7") || !strings.Contains(f.calls[0], `"orders"`) || !strings.Contains(f.calls[0], "sha256") {
		t.Fatalf("query = %q, want LIMIT 7, a quoted table and sha256", f.calls[0])
	}
	if h, n, err := NewPGSource(answers("0", nil)).SampleHash("orders", 20); err != nil || h != "" || n != 0 {
		t.Fatalf("no rows: SampleHash = %q, %d, %v, want empty hash, 0 rows, no error", h, n, err)
	}
	for _, bad := range []string{"20 hunter2", "x y z", "", "20 " + hash64 + " extra"} {
		if _, _, err := NewPGSource(answers(bad, nil)).SampleHash("orders", 20); err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("output %q: err = %v, want a safe error", bad, err)
		}
	}
	f = answers("1 "+hash64, nil)
	if _, _, err := NewPGSource(f).SampleHash("bad name", 20); err == nil || len(f.calls) != 0 {
		t.Fatalf("invalid identifier: err = %v, queries %v, want an error and no query", err, f.calls)
	}
}

func TestPGCanary(t *testing.T) {
	f := answers("2026-11-27T05:43:37.250000Z", nil)
	got, err := NewPGSource(f).Canary(config.Canary{Table: "rdr_canary", Column: "written_at"})
	want := time.Date(2026, 11, 27, 5, 43, 37, 250000000, time.UTC)
	if err != nil || !got.Equal(want) {
		t.Fatalf("Canary = %v, %v, want %v", got, err, want)
	}
	if !strings.Contains(f.calls[0], `"rdr_canary"`) || !strings.Contains(f.calls[0], `"written_at"`) || !strings.Contains(f.calls[0], "max(") {
		t.Fatalf("query = %q, want max() over the quoted table and column", f.calls[0])
	}
	if _, err := NewPGSource(answers("", nil)).Canary(config.Canary{Table: "rdr_canary", Column: "written_at"}); !errors.Is(err, ErrNoCanaryData) {
		t.Fatalf("empty canary table: err = %v, want ErrNoCanaryData", err)
	}
	_, err = NewPGSource(answers("hunter2", nil)).Canary(config.Canary{Table: "rdr_canary", Column: "written_at"})
	if err == nil || errors.Is(err, ErrNoCanaryData) || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("garbage canary: err = %v, want an error that is not ErrNoCanaryData and does not echo the value", err)
	}
	if _, err := NewPGSource(answers("", errors.New("boom"))).Canary(config.Canary{Table: "rdr_canary", Column: "written_at"}); err == nil || errors.Is(err, ErrNoCanaryData) {
		t.Fatalf("query error: err = %v, want an error that is not ErrNoCanaryData", err)
	}
	f = answers("x", nil)
	if _, err := NewPGSource(f).Canary(config.Canary{Path: "data/canary.txt"}); !errors.Is(err, ErrUnsupported) || len(f.calls) != 0 {
		t.Fatalf("path canary: err = %v, queries %v, want ErrUnsupported and no query", err, f.calls)
	}
	if _, err := NewPGSource(f).Canary(config.Canary{Table: "a b", Column: "c"}); err == nil || len(f.calls) != 0 {
		t.Fatalf("invalid identifier: err = %v, queries %v, want an error and no query", err, f.calls)
	}
}

func TestPGUnsupportedAbilities(t *testing.T) {
	f := answers("x", nil)
	p := NewPGSource(f)
	if _, err := p.PathExists("a"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("PathExists: %v, want ErrUnsupported", err)
	}
	if _, err := p.Command("ls"); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Command: %v, want ErrUnsupported", err)
	}
	if _, err := p.Integrity(); !errors.Is(err, ErrUnsupported) {
		t.Errorf("Integrity: %v, want ErrUnsupported", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("unsupported methods ran queries: %v", f.calls)
	}
}

func TestPGScalarPassesThrough(t *testing.T) {
	if v, err := NewPGSource(answers("7", nil)).Scalar("SELECT 7"); err != nil || v != "7" {
		t.Fatalf("Scalar = %q, %v, want 7", v, err)
	}
	if _, err := NewPGSource(answers("", errors.New("boom"))).Scalar("SELECT 7"); err == nil {
		t.Fatal("a query error was swallowed")
	}
}

// End to end through Run: the source answers like Postgres would.
func TestPGSourceThroughRun(t *testing.T) {
	f := &qfake{answer: func(sql string) (string, error) {
		switch {
		case strings.Contains(sql, "max("):
			return "2026-11-27T02:00:00.000000Z", nil
		case strings.Contains(sql, "sha256"):
			return "20 " + hash64, nil
		case strings.Contains(sql, "information_schema"):
			return "orders:id\norders:customer_id\norders:amount\norders:extra", nil
		case sql == "SELECT 1":
			return "0", nil
		default:
			return "50", nil
		}
	}}
	tg := target()
	tg.Structure.Paths = nil
	tg.AppChecks = []config.AppCheck{{Name: "q", Query: "SELECT 1", Expect: "0"}}
	rep := Run(tg, NewPGSource(f), now)
	for _, name := range []string{"structure:table:orders", "content:rows:orders", "content:hash:orders", "canary", "app:q"} {
		expect(t, find(t, rep, name), artifact.Pass, "")
	}
	for _, r := range rep.Results {
		if r.Check == "integrity" {
			t.Fatalf("Postgres produced an integrity result: %+v", r)
		}
	}
	if !rep.RPO.Known || rep.RPO.Age != 47*time.Minute+30*time.Second || rep.RPOStatus != measure.Within {
		t.Fatalf("RPO = %+v, status %v, want 47m30s within", rep.RPO, rep.RPOStatus)
	}
	if artifact.Overall(rep.Results) != artifact.Pass {
		t.Fatalf("Overall = %v: %+v", artifact.Overall(rep.Results), rep.Results)
	}
}
