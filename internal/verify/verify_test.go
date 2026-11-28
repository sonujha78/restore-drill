package verify

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/measure"
)

// ---------- a fake source: answers from maps, can be made to fail per method ----------

type fake struct {
	tables      map[string][]string
	paths       map[string]bool
	counts      map[string]int64
	hash        string
	hashRows    int
	canary      time.Time
	scalars     map[string]string
	commands    map[string]string
	integrity   string
	unsupported map[string]bool
	errs        map[string]error
}

func (f *fake) chk(m string) error {
	if f.unsupported[m] {
		return ErrUnsupported
	}
	return f.errs[m]
}

func (f *fake) Tables() (map[string][]string, error) {
	if err := f.chk("Tables"); err != nil {
		return nil, err
	}
	return f.tables, nil
}

func (f *fake) PathExists(rel string) (bool, error) {
	if err := f.chk("PathExists"); err != nil {
		return false, err
	}
	return f.paths[rel], nil
}

func (f *fake) RowCount(t string) (int64, error) {
	if err := f.chk("RowCount"); err != nil {
		return 0, err
	}
	n, ok := f.counts[t]
	if !ok {
		return 0, errors.New("no such table")
	}
	return n, nil
}

func (f *fake) SampleHash(t string, limit int) (string, int, error) {
	if err := f.chk("SampleHash"); err != nil {
		return "", 0, err
	}
	return f.hash, f.hashRows, nil
}

func (f *fake) Canary(c config.Canary) (time.Time, error) {
	if err := f.chk("Canary"); err != nil {
		return time.Time{}, err
	}
	return f.canary, nil
}

func (f *fake) Scalar(q string) (string, error) {
	if err := f.chk("Scalar"); err != nil {
		return "", err
	}
	v, ok := f.scalars[q]
	if !ok {
		return "", errors.New("query failed")
	}
	return v, nil
}

func (f *fake) Command(l string) (string, error) {
	if err := f.chk("Command"); err != nil {
		return "", err
	}
	v, ok := f.commands[l]
	if !ok {
		return "", errors.New("command refused")
	}
	return v, nil
}

func (f *fake) Integrity() (string, error) {
	if err := f.chk("Integrity"); err != nil {
		return "", err
	}
	return f.integrity, nil
}

func at(h, m, s int) time.Time { return time.Date(2026, 11, 27, h, m, s, 0, time.UTC) }

// Worked example: canary 02:00:00, drill clock 02:47:30 -> RPO 47m30s.
var now = at(2, 47, 30)

func healthy() *fake {
	return &fake{
		tables:      map[string][]string{"orders": {"id", "customer_id", "amount", "extra"}},
		paths:       map[string]bool{"data/a.txt": true},
		counts:      map[string]int64{"orders": 50},
		hash:        "abcdef0123456789",
		hashRows:    20,
		canary:      at(2, 0, 0),
		scalars:     map[string]string{"SELECT 1": "0"},
		commands:    map[string]string{"grep -c x f": "1"},
		integrity:   "ok",
		unsupported: map[string]bool{},
		errs:        map[string]error{},
	}
}

func target() *config.Target {
	return &config.Target{
		Name:   "t",
		Type:   "sqlite",
		RPO:    config.Duration{Duration: 2 * time.Hour},
		Canary: &config.Canary{Table: "rdr_canary", Column: "written_at"},
		Structure: config.Structure{
			Tables: []config.TableSpec{{Name: "orders", Columns: []string{"id", "amount"}}},
			Paths:  []string{"data/a.txt"},
		},
		Content: config.Content{RowCounts: []config.RowCount{{Table: "orders", Min: 10, Max: 100}}},
		AppChecks: []config.AppCheck{
			{Name: "q", Query: "SELECT 1", Expect: "0"},
			{Name: "c", Command: "grep -c x f", Expect: "1"},
		},
	}
}

func find(t *testing.T, rep Report, check string) artifact.Result {
	t.Helper()
	for _, r := range rep.Results {
		if r.Check == check {
			return r
		}
	}
	t.Fatalf("no result named %q in %+v", check, rep.Results)
	return artifact.Result{}
}

func expect(t *testing.T, r artifact.Result, s artifact.Status, contains string) {
	t.Helper()
	if r.Status != s {
		t.Fatalf("%s: status = %v (detail %q), want %v", r.Check, r.Status, r.Detail, s)
	}
	if !strings.Contains(r.Detail, contains) {
		t.Fatalf("%s: detail = %q, want it to contain %q", r.Check, r.Detail, contains)
	}
}

func overall(rep Report) artifact.Status { return artifact.Overall(rep.Results) }

// ---------- healthy ----------

func TestHealthyBackupPassesEverythingAndMeasuresRPO(t *testing.T) {
	rep := Run(target(), healthy(), now)
	if overall(rep) != artifact.Pass {
		t.Fatalf("Overall = %v for a healthy backup: %+v", overall(rep), rep.Results)
	}
	for _, name := range []string{"integrity", "structure:table:orders", "structure:path:data/a.txt",
		"content:rows:orders", "content:hash:orders", "canary", "app:q", "app:c"} {
		expect(t, find(t, rep, name), artifact.Pass, "")
	}
	if !rep.RPO.Known || rep.RPO.Age != 47*time.Minute+30*time.Second {
		t.Fatalf("RPO = %+v, want known 47m30s", rep.RPO)
	}
	if rep.RPOStatus != measure.Within {
		t.Fatalf("RPOStatus = %v, want within", rep.RPOStatus)
	}
	if rep.Canary == nil || !rep.Canary.Equal(at(2, 0, 0)) {
		t.Fatalf("Canary = %v, want 02:00:00", rep.Canary)
	}
}

func TestRunIsDeterministic(t *testing.T) {
	a, b := Run(target(), healthy(), now), Run(target(), healthy(), now)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("two runs on the same input differ:\n%+v\n%+v", a, b)
	}
}

// ---------- structure ----------

func TestStructureFaults(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(f *fake)
		check  string
		status artifact.Status
		detail string
	}{
		{"table missing", func(f *fake) { delete(f.tables, "orders") }, "structure:table:orders", artifact.Fail, "table is missing"},
		{"column missing", func(f *fake) { f.tables["orders"] = []string{"id"} }, "structure:table:orders", artifact.Fail, "amount"},
		{"path missing", func(f *fake) { f.paths["data/a.txt"] = false }, "structure:path:data/a.txt", artifact.Fail, "path is missing"},
		{"tables not supported", func(f *fake) { f.unsupported["Tables"] = true }, "structure:table:orders", artifact.Unverified, "cannot"},
		{"paths not supported", func(f *fake) { f.unsupported["PathExists"] = true }, "structure:path:data/a.txt", artifact.Unverified, "cannot"},
		{"table listing fails", func(f *fake) { f.errs["Tables"] = errors.New("boom") }, "structure:table:orders", artifact.Unverified, "could not run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := healthy()
			c.mod(f)
			rep := Run(target(), f, now)
			expect(t, find(t, rep, c.check), c.status, c.detail)
			if overall(rep) == artifact.Pass {
				t.Fatal("a backup with a structure problem was reported as pass")
			}
		})
	}
}

func TestExtraColumnsAndCaseDifferencesAreFine(t *testing.T) {
	f := healthy()
	f.tables = map[string][]string{"Orders": {"ID", "Amount", "whatever"}}
	expect(t, find(t, Run(target(), f, now), "structure:table:orders"), artifact.Pass, "")
}

// ---------- content ----------

func TestRowCountRange(t *testing.T) {
	cases := []struct {
		n      int64
		status artifact.Status
		detail string
	}{
		{0, artifact.Fail, "0 rows"},
		{9, artifact.Fail, "9 rows"},
		{10, artifact.Pass, "10 rows"},
		{100, artifact.Pass, "100 rows"},
		{101, artifact.Fail, "101 rows"},
	}
	for _, c := range cases {
		f := healthy()
		f.counts["orders"] = c.n
		expect(t, find(t, Run(target(), f, now), "content:rows:orders"), c.status, c.detail)
	}
	f := healthy()
	delete(f.counts, "orders")
	expect(t, find(t, Run(target(), f, now), "content:rows:orders"), artifact.Unverified, "could not run")
}

func TestSampleHashIsEvidenceAndNeverPrintsRows(t *testing.T) {
	rep := Run(target(), healthy(), now)
	r := find(t, rep, "content:hash:orders")
	expect(t, r, artifact.Pass, "not compared")
	if rep.SampleHashes["orders"] != "abcdef0123456789" {
		t.Fatalf("SampleHashes = %v, want the hash recorded", rep.SampleHashes)
	}
	f := healthy()
	f.hashRows = 0
	expect(t, find(t, Run(target(), f, now), "content:hash:orders"), artifact.Unverified, "no rows")
}

// ---------- canary and RPO ----------

func TestNoCanaryConfiguredIsUnverifiedAndRPOIsUnknown(t *testing.T) {
	tg := target()
	tg.Canary = nil
	rep := Run(tg, healthy(), now)
	expect(t, find(t, rep, "canary"), artifact.Unverified, "RPO cannot be measured")
	if rep.RPO.Known || rep.RPO.Reason == "" {
		t.Fatalf("RPO = %+v, want unknown with a reason", rep.RPO)
	}
	if rep.RPOStatus != measure.Unknown {
		t.Fatalf("RPOStatus = %v, want unknown", rep.RPOStatus)
	}
	if overall(rep) != artifact.Unverified {
		t.Fatalf("Overall = %v, want Unverified (every other check passed)", overall(rep))
	}
}

func TestCanaryProblems(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(f *fake)
		status artifact.Status
		detail string
	}{
		{"no canary row", func(f *fake) { f.errs["Canary"] = ErrNoCanaryData }, artifact.Fail, "no canary row"},
		{"not supported", func(f *fake) { f.unsupported["Canary"] = true }, artifact.Unverified, "cannot"},
		{"read error", func(f *fake) { f.errs["Canary"] = errors.New("boom") }, artifact.Unverified, "could not read"},
		{"canary in the future", func(f *fake) { f.canary = at(3, 0, 0) }, artifact.Unverified, "future"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := healthy()
			c.mod(f)
			rep := Run(target(), f, now)
			expect(t, find(t, rep, "canary"), c.status, c.detail)
			if overall(rep) == artifact.Pass {
				t.Fatal("a backup with a canary problem was reported as pass")
			}
		})
	}
}

func TestStaleBackupPassesChecksButBreachesRPO(t *testing.T) {
	f := healthy()
	f.canary = now.Add(-5 * time.Hour)
	rep := Run(target(), f, now)
	if overall(rep) != artifact.Pass {
		t.Fatalf("Overall = %v, want Pass: a stale backup is intact, only its RPO is bad", overall(rep))
	}
	if rep.RPO.Age != 5*time.Hour || rep.RPOStatus != measure.Over {
		t.Fatalf("RPO = %v, status %v, want 5h and over", rep.RPO.Age, rep.RPOStatus)
	}
	f.canary = now.Add(-2 * time.Hour)
	if got := Run(target(), f, now).RPOStatus; got != measure.Within {
		t.Fatalf("RPO exactly at target = %v, want within", got)
	}
}

func TestEmptyButValidBackupFails(t *testing.T) {
	f := healthy()
	f.counts["orders"] = 0
	f.errs["Canary"] = ErrNoCanaryData
	rep := Run(target(), f, now)
	expect(t, find(t, rep, "structure:table:orders"), artifact.Pass, "")
	expect(t, find(t, rep, "content:rows:orders"), artifact.Fail, "0 rows")
	expect(t, find(t, rep, "canary"), artifact.Fail, "no canary row")
	if overall(rep) != artifact.Fail {
		t.Fatalf("Overall = %v, want Fail", overall(rep))
	}
}

// ---------- app checks ----------

func TestAppChecks(t *testing.T) {
	f := healthy()
	f.scalars["SELECT 1"] = "7"
	expect(t, find(t, Run(target(), f, now), "app:q"), artifact.Fail, "returned 7, expected 0")

	f = healthy()
	f.scalars["SELECT 1"] = "hunter2"
	r := find(t, Run(target(), f, now), "app:q")
	expect(t, r, artifact.Fail, "withheld")
	if strings.Contains(r.Detail, "hunter2") {
		t.Fatalf("a non-numeric query result leaked: %q", r.Detail)
	}

	f = healthy()
	delete(f.scalars, "SELECT 1")
	expect(t, find(t, Run(target(), f, now), "app:q"), artifact.Unverified, "could not run")

	f = healthy()
	f.unsupported["Command"] = true
	expect(t, find(t, Run(target(), f, now), "app:c"), artifact.Unverified, "cannot")

	f = healthy()
	f.commands["grep -c x f"] = "0"
	expect(t, find(t, Run(target(), f, now), "app:c"), artifact.Fail, "returned 0, expected 1")
}

// ---------- integrity ----------

func TestIntegrity(t *testing.T) {
	f := healthy()
	f.integrity = "*** in database main ***"
	expect(t, find(t, Run(target(), f, now), "integrity"), artifact.Fail, "reported a problem")

	f = healthy()
	f.errs["Integrity"] = errors.New("database disk image is malformed")
	expect(t, find(t, Run(target(), f, now), "integrity"), artifact.Fail, "could not be read")

	f = healthy()
	f.unsupported["Integrity"] = true
	for _, r := range Run(target(), f, now).Results {
		if r.Check == "integrity" {
			t.Fatalf("a source without integrity support produced an integrity result: %+v", r)
		}
	}
}

func TestNothingConfiguredIsNeverAPass(t *testing.T) {
	rep := Run(&config.Target{}, healthy(), now)
	if overall(rep) != artifact.Unverified {
		t.Fatalf("Overall = %v, want Unverified when nothing could be checked, only a missing canary", overall(rep))
	}
}
