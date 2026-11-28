package drill

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/verify"
)

func at(h, m, s int) time.Time            { return time.Date(2026, 11, 27, h, m, s, 0, time.UTC) }
func dur(d time.Duration) config.Duration { return config.Duration{Duration: d} }

// ---------- fakes ----------

type stubSource struct {
	rows      int64
	canary    time.Time
	canaryErr error
}

func (s *stubSource) Tables() (map[string][]string, error) {
	return map[string][]string{"orders": {"id", "amount"}}, nil
}
func (s *stubSource) PathExists(string) (bool, error) { return false, verify.ErrUnsupported }
func (s *stubSource) RowCount(string) (int64, error)  { return s.rows, nil }
func (s *stubSource) SampleHash(string, int) (string, int, error) {
	return strings.Repeat("ab", 32), 3, nil
}
func (s *stubSource) Canary(config.Canary) (time.Time, error) {
	if s.canaryErr != nil {
		return time.Time{}, s.canaryErr
	}
	return s.canary, nil
}
func (s *stubSource) Scalar(string) (string, error)  { return "", verify.ErrUnsupported }
func (s *stubSource) Command(string) (string, error) { return "", verify.ErrUnsupported }
func (s *stubSource) Integrity() (string, error)     { return "", verify.ErrUnsupported }

type fakeSession struct {
	clk              *clock.Fake
	restoreFor       time.Duration
	restoreErr       error
	src              *stubSource
	closeErr         error
	restored, closed int
}

func (f *fakeSession) Restore(string) error {
	f.restored++
	f.clk.Advance(f.restoreFor)
	return f.restoreErr
}
func (f *fakeSession) Source() (verify.Source, error) { return f.src, nil }
func (f *fakeSession) Close() error                   { f.closed++; return f.closeErr }

func passArtifact(artifact.Params) []artifact.Result {
	return []artifact.Result{
		{Check: "exists", Status: artifact.Pass, Detail: "regular file"},
		{Check: "checksum", Status: artifact.Pass, Detail: "sha256 matches manifest"},
	}
}

// setup builds a config with one target of a unique fake kind. Registering a
// kind is the only thing a new backup type needs, which is what this proves.
func setup(t *testing.T, proves bool, mod func(f *fakeSession, tg *config.Target)) (*config.Config, Options, *fakeSession, *int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "b-20261127.bak"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(at(10, 0, 0))
	sess := &fakeSession{clk: clk, restoreFor: 400 * time.Second, src: &stubSource{rows: 50, canary: at(9, 12, 30)}}
	tg := &config.Target{
		Name:      "t1",
		Type:      "fake-" + strings.ReplaceAll(t.Name(), "/", "-"),
		Location:  config.Location{Dir: dir, Pattern: "b-*.bak"},
		Manifest:  "SHA256SUMS",
		RPO:       dur(2 * time.Hour),
		RTO:       dur(10 * time.Minute),
		Canary:    &config.Canary{Table: "rdr_canary", Column: "written_at"},
		Structure: config.Structure{Tables: []config.TableSpec{{Name: "orders", Columns: []string{"id"}}}},
		Content:   config.Content{RowCounts: []config.RowCount{{Table: "orders", Min: 10, Max: 100}}},
	}
	if mod != nil {
		mod(sess, tg)
	}
	calls := 0
	Register(tg.Type, Adapter{
		New:                   func(*config.Target, Env) (Session, error) { calls++; return sess, nil },
		RestoreProvesReadable: proves,
	})
	t.Cleanup(func() { Unregister(tg.Type) })
	opt := Options{Clock: clk, SandboxBase: t.TempDir(), Artifact: passArtifact}
	return &config.Config{Targets: []config.Target{*tg}}, opt, sess, &calls
}

func mustRun(t *testing.T, cfg *config.Config, opt Options) *Result {
	t.Helper()
	res, err := Run(cfg, opt)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if len(res.Targets) < 1 {
		t.Fatalf("len(Targets) = %d, want at least 1", len(res.Targets))
	}
	return res
}

func checkOf(t *testing.T, tr TargetResult, name string) CheckResult {
	t.Helper()
	for _, c := range tr.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, tr.Checks)
	return CheckResult{}
}

func reasons(tr TargetResult) string { return strings.Join(tr.Reasons, " | ") }

// ---------- healthy: the worked examples ----------

// Drill clock 10:00:00, canary 09:12:30 -> RPO 47m30s.
// Restore starts 10:00:00, last check passes 10:06:40 -> RTO 400s.
func TestDrillHealthyTargetIsVerifiedWithTheWorkedNumbers(t *testing.T) {
	cfg, opt, sess, _ := setup(t, false, nil)
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusVerified {
		t.Fatalf("Status = %q (reasons %s), want verified", tr.Status, reasons(tr))
	}
	if tr.RTONs == nil || time.Duration(*tr.RTONs) != 400*time.Second || tr.RTOStatus != "within" {
		t.Fatalf("RTO = %v, status %q, want 400s within", tr.RTONs, tr.RTOStatus)
	}
	if tr.RPONs == nil || time.Duration(*tr.RPONs) != 47*time.Minute+30*time.Second || tr.RPOStatus != "within" {
		t.Fatalf("RPO = %v, status %q, want 47m30s within", tr.RPONs, tr.RPOStatus)
	}
	if res.Gate.Exit != exitcode.OK || res.Gate.Decision != "ok" {
		t.Fatalf("Gate = %+v, want exit 0 ok", res.Gate)
	}
	if tr.File != "b-20261127.bak" {
		t.Fatalf("File = %q, want the base name only", tr.File)
	}
	if res.StartedAt != "2026-11-27T10:00:00Z" || res.FinishedAt != "2026-11-27T10:06:40Z" {
		t.Fatalf("started %q finished %q, want 10:00:00Z and 10:06:40Z", res.StartedAt, res.FinishedAt)
	}
	if sess.closed != 1 {
		t.Fatalf("Close() ran %d times, want 1", sess.closed)
	}
}

func TestDrillRTOOverTargetIsDegradedButStillVerified(t *testing.T) {
	cfg, opt, _, _ := setup(t, false, func(f *fakeSession, tg *config.Target) { tg.RTO = dur(60 * time.Second) })
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusVerified || tr.RTOStatus != "over" {
		t.Fatalf("Status %q, RTO status %q, want verified and over", tr.Status, tr.RTOStatus)
	}
	if res.Gate.Exit != exitcode.Degraded {
		t.Fatalf("Gate exit = %d, want 1", res.Gate.Exit)
	}
}

func TestDrillStaleBackupIsDegradedButStillVerified(t *testing.T) {
	cfg, opt, sess, _ := setup(t, false, nil)
	sess.src.canary = at(5, 0, 0)
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusVerified || tr.RPOStatus != "over" || tr.RPONs == nil || time.Duration(*tr.RPONs) != 5*time.Hour {
		t.Fatalf("Status %q, RPO %v %q, want verified, 5h, over", tr.Status, tr.RPONs, tr.RPOStatus)
	}
	if res.Gate.Exit != exitcode.Degraded {
		t.Fatalf("Gate exit = %d, want 1", res.Gate.Exit)
	}
}

// ---------- unverified is never verified ----------

func TestDrillNoCanaryIsUnverifiedAndSaysWhy(t *testing.T) {
	cfg, opt, _, _ := setup(t, false, func(f *fakeSession, tg *config.Target) { tg.Canary = nil })
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusUnverified {
		t.Fatalf("Status = %q, want unverified", tr.Status)
	}
	if tr.RPONs != nil || tr.RPOStatus != "unknown" || tr.RPOReason == "" {
		t.Fatalf("RPO = %v %q reason %q, want null, unknown and a reason", tr.RPONs, tr.RPOStatus, tr.RPOReason)
	}
	if !strings.Contains(reasons(tr), "RPO cannot be measured") {
		t.Fatalf("reasons = %q, want 'RPO cannot be measured'", reasons(tr))
	}
	if res.Gate.Exit != exitcode.Degraded {
		t.Fatalf("Gate exit = %d, want 1", res.Gate.Exit)
	}
}

func TestDrillUnregisteredAdapterIsUnverified(t *testing.T) {
	cfg, opt, _, calls := setup(t, false, nil)
	Unregister(cfg.Targets[0].Type)
	tr := mustRun(t, cfg, opt).Targets[0]
	if tr.Status != StatusUnverified || !strings.Contains(reasons(tr), "no adapter") || *calls != 0 {
		t.Fatalf("Status %q, reasons %q, factory calls %d, want unverified, 'no adapter', 0", tr.Status, reasons(tr), *calls)
	}
}

func TestDrillFactoryErrorIsUnverifiedNotAPass(t *testing.T) {
	cfg, opt, _, _ := setup(t, false, nil)
	Register(cfg.Targets[0].Type, Adapter{New: func(*config.Target, Env) (Session, error) {
		return nil, errors.New("refusing: container name must match rdr-")
	}})
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusUnverified || !strings.Contains(reasons(tr), "refusing") {
		t.Fatalf("Status %q, reasons %q, want unverified and 'refusing'", tr.Status, reasons(tr))
	}
	if res.Gate.Exit != exitcode.Degraded {
		t.Fatalf("Gate exit = %d, want 1", res.Gate.Exit)
	}
}

// ---------- failures ----------

func TestDrillRestoreFailureIsFailedAndStillCleansUp(t *testing.T) {
	cfg, opt, sess, _ := setup(t, false, func(f *fakeSession, tg *config.Target) {})
	sess.restoreErr = errors.New("pg_restore failed: boom")
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusFailed || tr.RTONs != nil || tr.RTOStatus != "unknown" {
		t.Fatalf("Status %q, RTO %v %q, want failed with no RTO", tr.Status, tr.RTONs, tr.RTOStatus)
	}
	if c := checkOf(t, tr, "restore"); c.Status != "fail" || !strings.Contains(c.Detail, "boom") {
		t.Fatalf("restore check = %+v, want fail with the error", c)
	}
	if sess.closed != 1 || res.Gate.Exit != exitcode.NotRestorable {
		t.Fatalf("closed %d, gate exit %d, want 1 and 2", sess.closed, res.Gate.Exit)
	}
}

func TestDrillEmptyButValidBackupFails(t *testing.T) {
	cfg, opt, sess, _ := setup(t, false, nil)
	sess.src.rows = 0
	sess.src.canaryErr = verify.ErrNoCanaryData
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusFailed || res.Gate.Exit != exitcode.NotRestorable {
		t.Fatalf("Status %q, gate %d, want failed and 2", tr.Status, res.Gate.Exit)
	}
	if checkOf(t, tr, "restore").Status != "pass" || checkOf(t, tr, "content:rows:orders").Status != "fail" || checkOf(t, tr, "canary").Status != "fail" {
		t.Fatalf("checks = %+v, want restore pass, rows fail, canary fail", tr.Checks)
	}
}

func TestDrillArtifactFailureStopsBeforeRestore(t *testing.T) {
	cfg, opt, sess, calls := setup(t, false, nil)
	opt.Artifact = func(artifact.Params) []artifact.Result {
		return []artifact.Result{
			{Check: "exists", Status: artifact.Pass, Detail: "regular file"},
			{Check: "checksum", Status: artifact.Fail, Detail: "sha256 differs from manifest"},
		}
	}
	res := mustRun(t, cfg, opt)
	tr := res.Targets[0]
	if tr.Status != StatusFailed || *calls != 0 || sess.restored != 0 {
		t.Fatalf("Status %q, factory calls %d, restores %d, want failed, 0, 0", tr.Status, *calls, sess.restored)
	}
	if c := checkOf(t, tr, "restore"); c.Status != "unverified" || !strings.Contains(c.Detail, "not attempted") {
		t.Fatalf("restore check = %+v, want unverified 'not attempted'", c)
	}
	if !strings.Contains(reasons(tr), "checksum") || res.Gate.Exit != exitcode.NotRestorable {
		t.Fatalf("reasons %q, gate %d, want the checksum named and exit 2", reasons(tr), res.Gate.Exit)
	}
}

func TestDrillNoBackupFileIsFailed(t *testing.T) {
	cfg, opt, _, calls := setup(t, false, nil)
	if err := os.Remove(filepath.Join(cfg.Targets[0].Location.Dir, "b-20261127.bak")); err != nil {
		t.Fatal(err)
	}
	tr := mustRun(t, cfg, opt).Targets[0]
	if tr.Status != StatusFailed || checkOf(t, tr, "exists").Status != "fail" || *calls != 0 {
		t.Fatalf("Status %q, checks %+v, factory calls %d, want failed, exists fail, 0", tr.Status, tr.Checks, *calls)
	}
}

// ---------- readable is proven by the restore only when the adapter says so ----------

func unverifiedReadable(artifact.Params) []artifact.Result {
	return append(passArtifact(artifact.Params{}),
		artifact.Result{Check: "readable", Status: artifact.Unverified, Detail: "pg_restore --list has not been run"})
}

func TestDrillRestoreUpgradesReadableOnlyWhenTheAdapterProvesIt(t *testing.T) {
	cfg, opt, _, _ := setup(t, true, nil)
	opt.Artifact = unverifiedReadable
	tr := mustRun(t, cfg, opt).Targets[0]
	if c := checkOf(t, tr, "readable"); c.Status != "pass" || !strings.Contains(c.Detail, "restore") {
		t.Fatalf("readable = %+v, want pass explained by the restore", c)
	}
	if tr.Status != StatusVerified {
		t.Fatalf("Status = %q (%s), want verified", tr.Status, reasons(tr))
	}

	cfg2, opt2, _, _ := setup(t, false, nil)
	opt2.Artifact = unverifiedReadable
	tr2 := mustRun(t, cfg2, opt2).Targets[0]
	if checkOf(t, tr2, "readable").Status != "unverified" || tr2.Status != StatusUnverified {
		t.Fatalf("without RestoreProvesReadable: readable %+v, status %q, want unverified", checkOf(t, tr2, "readable"), tr2.Status)
	}
}

func TestDrillFailedRestoreDoesNotUpgradeReadable(t *testing.T) {
	cfg, opt, sess, _ := setup(t, true, nil)
	opt.Artifact = unverifiedReadable
	sess.restoreErr = errors.New("boom")
	tr := mustRun(t, cfg, opt).Targets[0]
	if checkOf(t, tr, "readable").Status != "unverified" || tr.Status != StatusFailed {
		t.Fatalf("readable %+v, status %q, want unverified and failed", checkOf(t, tr, "readable"), tr.Status)
	}
}

// ---------- cleanup, selection ----------

func TestDrillCleanupFailureIsANoteNotAStatusChange(t *testing.T) {
	cfg, opt, sess, _ := setup(t, false, nil)
	sess.closeErr = errors.New("could not remove sandbox")
	tr := mustRun(t, cfg, opt).Targets[0]
	if tr.Status != StatusVerified || !strings.Contains(strings.Join(tr.Notes, " "), "cleanup failed") {
		t.Fatalf("Status %q, notes %v, want verified and a 'cleanup failed' note", tr.Status, tr.Notes)
	}
}

func TestDrillTargetSelection(t *testing.T) {
	cfg, opt, _, _ := setup(t, false, nil)
	second := cfg.Targets[0]
	second.Name = "t2"
	cfg.Targets = append(cfg.Targets, second)

	opt.Targets = []string{"t2"}
	res := mustRun(t, cfg, opt)
	if len(res.Targets) != 1 || res.Targets[0].Name != "t2" {
		t.Fatalf("targets = %+v, want only t2", res.Targets)
	}
	opt.Targets = nil
	if res := mustRun(t, cfg, opt); len(res.Targets) != 2 {
		t.Fatalf("len(Targets) = %d, want 2 when none are named", len(res.Targets))
	}
	opt.Targets = []string{"nope"}
	if _, err := Run(cfg, opt); err == nil || !strings.Contains(err.Error(), "no target named") {
		t.Fatalf("unknown target: err = %v, want 'no target named'", err)
	}
	opt.Targets = nil
	opt.Clock = nil
	if _, err := Run(cfg, opt); err == nil {
		t.Fatal("Run without a clock succeeded")
	}
}

func TestDrillKindsListsRegisteredKinds(t *testing.T) {
	cfg, _, _, _ := setup(t, false, nil)
	found := false
	for _, k := range Kinds() {
		if k == cfg.Targets[0].Type {
			found = true
		}
	}
	if !found {
		t.Fatalf("Kinds() = %v, want it to contain %q", Kinds(), cfg.Targets[0].Type)
	}
}

// ---------- the gate ----------

func TestDecideGateTable(t *testing.T) {
	mk := func(name, status, rto, rpo string) TargetResult {
		return TargetResult{Name: name, Status: status, RTOStatus: rto, RPOStatus: rpo}
	}
	cases := []struct {
		name     string
		in       []TargetResult
		exit     int
		decision string
	}{
		{"all verified and within", []TargetResult{mk("alpha", "verified", "within", "within"), mk("beta", "verified", "within", "within")}, 0, "ok"},
		{"RTO over", []TargetResult{mk("alpha", "verified", "over", "within")}, 1, "degraded"},
		{"RPO over", []TargetResult{mk("alpha", "verified", "within", "over")}, 1, "degraded"},
		{"RTO not measured", []TargetResult{mk("alpha", "verified", "unknown", "within")}, 1, "degraded"},
		{"RPO not measured", []TargetResult{mk("alpha", "verified", "within", "unknown")}, 1, "degraded"},
		{"unverified target", []TargetResult{mk("alpha", "unverified", "within", "unknown")}, 1, "degraded"},
		{"failed target", []TargetResult{mk("alpha", "failed", "unknown", "unknown")}, 2, "not_restorable"},
		{"failed beats degraded", []TargetResult{mk("alpha", "verified", "over", "within"), mk("beta", "failed", "unknown", "unknown")}, 2, "not_restorable"},
		{"unverified plus failed", []TargetResult{mk("alpha", "unverified", "within", "unknown"), mk("beta", "failed", "unknown", "unknown")}, 2, "not_restorable"},
		{"unknown status string", []TargetResult{mk("alpha", "bogus", "within", "within")}, 1, "degraded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := Decide(c.in)
			if g.Exit != c.exit || g.Decision != c.decision {
				t.Fatalf("Decide = exit %d %q, want %d %q", g.Exit, g.Decision, c.exit, c.decision)
			}
			if (c.exit == 0) != (len(g.Reasons) == 0) {
				t.Fatalf("reasons = %v, want none only for an ok gate", g.Reasons)
			}
		})
	}
	if g := Decide(nil); g.Exit != exitcode.ToolError {
		t.Fatalf("Decide(nil) exit = %d, want %d: a drill of nothing must not look fine", g.Exit, exitcode.ToolError)
	}
}

// ---------- the recorded result ----------

func TestDrillResultJSONIsDeterministicAndRoundTrips(t *testing.T) {
	cfg, opt, _, _ := setup(t, false, nil)
	res := mustRun(t, cfg, opt)
	a, err := res.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := res.Marshal()
	if !bytes.Equal(a, b) {
		t.Fatal("two Marshal calls on the same result differ")
	}
	var back Result
	if err := json.Unmarshal(a, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	c, _ := back.Marshal()
	if !bytes.Equal(a, c) || back.Version != ResultVersion || len(back.Targets) != 1 {
		t.Fatalf("round trip changed the result (version %d, %d targets)", back.Version, len(back.Targets))
	}
	js := string(a)
	for _, leak := range []string{cfg.Targets[0].Location.Dir, opt.SandboxBase} {
		if strings.Contains(js, leak) {
			t.Fatalf("the result holds a machine path %q", leak)
		}
	}
}

func TestDrillResultFileIsWrittenAndNeverOverwritten(t *testing.T) {
	cfg, opt, _, _ := setup(t, false, nil)
	res := mustRun(t, cfg, opt)
	dir := filepath.Join(t.TempDir(), "out")
	p1, err := res.Write(dir)
	if err != nil || filepath.Base(p1) != "drill-20261127T100000Z.json" {
		t.Fatalf("Write = %q, %v, want drill-20261127T100000Z.json", p1, err)
	}
	want, _ := res.Marshal()
	got, err := os.ReadFile(p1)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("file content differs from Marshal (%v)", err)
	}
	if st, _ := os.Stat(p1); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", st.Mode().Perm())
	}
	p2, err := res.Write(dir)
	if err != nil || p2 == p1 || filepath.Base(p2) != "drill-20261127T100000Z-2.json" {
		t.Fatalf("second Write = %q, %v, want drill-20261127T100000Z-2.json", p2, err)
	}
	if again, _ := os.ReadFile(p1); !bytes.Equal(again, want) {
		t.Fatal("the first result file was changed by the second Write")
	}
}
