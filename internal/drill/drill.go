package drill

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/measure"
	"github.com/sonujha78/restore-drill/internal/verify"
)

var adapters = map[string]Adapter{}

// Register adds the adapter of a backup type. Adapters call it from init(),
// and that is all a new backup type needs from this package.
func Register(kind string, a Adapter) { adapters[kind] = a }

// Unregister removes an adapter (tests).
func Unregister(kind string) { delete(adapters, kind) }

// Kinds lists the registered backup types, sorted.
func Kinds() []string {
	out := make([]string, 0, len(adapters))
	for k := range adapters {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func short(err error) string {
	s := strings.SplitN(err.Error(), "\n", 2)[0]
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

func pickBackup(t *config.Target) (string, bool, error) {
	matches, err := filepath.Glob(filepath.Join(t.Location.Dir, t.Location.Pattern))
	if err != nil {
		return "", false, err
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	sort.Strings(matches) // names carry a UTC timestamp, so the last one is the newest
	return matches[len(matches)-1], true, nil
}

func statusOf(rs []artifact.Result) string {
	switch artifact.Overall(rs) {
	case artifact.Pass:
		return StatusVerified
	case artifact.Fail:
		return StatusFailed
	default:
		return StatusUnverified
	}
}

func anyFail(rs []artifact.Result) bool {
	for _, r := range rs {
		if r.Status == artifact.Fail {
			return true
		}
	}
	return false
}

// proveReadable turns an unverified "readable" check into a pass, but only
// after a successful restore and only for adapters that say a restore proves it.
func proveReadable(rs []artifact.Result) []artifact.Result {
	for i := range rs {
		if rs[i].Check == "readable" && rs[i].Status == artifact.Unverified {
			rs[i].Status = artifact.Pass
			rs[i].Detail = "the restore completed successfully, so the backup was read end to end (this does not prove the data inside is uncorrupted)"
		}
	}
	return rs
}

func selectTargets(cfg *config.Config, names []string) ([]*config.Target, error) {
	want := map[string]bool{}
	for _, n := range names {
		found := false
		for i := range cfg.Targets {
			if cfg.Targets[i].Name == n {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("no target named %q in the config", n)
		}
		want[n] = true
	}
	var out []*config.Target
	for i := range cfg.Targets {
		if len(want) == 0 || want[cfg.Targets[i].Name] {
			out = append(out, &cfg.Targets[i])
		}
	}
	return out, nil
}

// Run drills the selected targets one after another and decides the gate.
func Run(cfg *config.Config, opt Options) (*Result, error) {
	if opt.Clock == nil {
		return nil, errors.New("drill needs a clock")
	}
	art := opt.Artifact
	if art == nil {
		art = artifact.Run
	}
	selected, err := selectTargets(cfg, opt.Targets)
	if err != nil {
		return nil, err
	}
	started := opt.Clock.Now()
	res := &Result{Version: ResultVersion, StartedAt: ts(started), Targets: []TargetResult{}}
	for _, t := range selected {
		res.Targets = append(res.Targets, drillTarget(t, started, opt, art))
	}
	res.FinishedAt = ts(opt.Clock.Now())
	res.Gate = Decide(res.Targets)
	return res, nil
}

func drillTarget(t *config.Target, now time.Time, opt Options, art func(artifact.Params) []artifact.Result) TargetResult {
	tr := TargetResult{
		Name: t.Name, Type: t.Type,
		Checks: []CheckResult{}, Reasons: []string{}, Notes: []string{}, SampleHashes: map[string]string{},
		RPOTargetNs: int64(t.RPO.Duration), RTOTargetNs: int64(t.RTO.Duration),
		RPOStatus: measure.Unknown.String(), RTOStatus: measure.Unknown.String(),
	}
	var all []artifact.Result
	finish := func() TargetResult {
		for _, r := range all {
			tr.Checks = append(tr.Checks, CheckResult{Name: r.Check, Status: r.Status.String(), Detail: r.Detail})
			if r.Status != artifact.Pass {
				tr.Reasons = append(tr.Reasons, fmt.Sprintf("%s %s: %s", r.Status, r.Check, r.Detail))
			}
		}
		tr.Status = statusOf(all)
		return tr
	}

	path, found, err := pickBackup(t)
	switch {
	case err != nil:
		all = append(all, artifact.Result{Check: "exists", Status: artifact.Unverified, Detail: "bad location pattern: " + short(err)})
		return finish()
	case !found:
		all = append(all, artifact.Result{Check: "exists", Status: artifact.Fail, Detail: "no file matches " + t.Location.Pattern})
		return finish()
	}
	tr.File = filepath.Base(path)

	manifest := ""
	if t.Manifest != "" {
		manifest = filepath.Join(t.Location.Dir, t.Manifest)
	}
	all = append(all, art(artifact.Params{Kind: t.Type, Path: path, ManifestPath: manifest, MinBytes: t.MinBytes})...)
	if artifact.Overall(all) == artifact.Fail {
		all = append(all, artifact.Result{Check: "restore", Status: artifact.Unverified, Detail: "not attempted because an artifact check failed"})
		return finish()
	}

	ad, ok := adapters[t.Type]
	if !ok {
		all = append(all, artifact.Result{Check: "sandbox", Status: artifact.Unverified, Detail: "no adapter registered for type " + t.Type})
		return finish()
	}
	sess, err := ad.New(t, Env{Clock: opt.Clock, SandboxBase: opt.SandboxBase})
	if err != nil {
		all = append(all, artifact.Result{Check: "sandbox", Status: artifact.Unverified, Detail: "could not start a sandbox: " + short(err)})
		return finish()
	}
	cleanup := func() {
		if cerr := sess.Close(); cerr != nil {
			tr.Notes = append(tr.Notes, "cleanup failed: "+short(cerr))
		}
	}

	start := opt.Clock.Now()
	if rerr := sess.Restore(path); rerr != nil {
		all = append(all, artifact.Result{Check: "restore", Status: artifact.Fail, Detail: short(rerr)})
		cleanup()
		return finish()
	}
	if ad.RestoreProvesReadable {
		all = proveReadable(all)
	}
	all = append(all, artifact.Result{Check: "restore", Status: artifact.Pass, Detail: "restored into a throwaway sandbox"})

	src, err := sess.Source()
	if err != nil {
		all = append(all, artifact.Result{Check: "verify", Status: artifact.Fail, Detail: "restored data could not be opened: " + short(err)})
		cleanup()
		return finish()
	}
	rep := verify.Run(t, src, now)
	end := opt.Clock.Now()
	cleanup()
	all = append(all, rep.Results...)

	for k, v := range rep.SampleHashes {
		tr.SampleHashes[k] = v
	}
	if rep.Canary != nil {
		tr.CanaryAt = ts(*rep.Canary)
	}
	if rep.RPO.Known {
		n := int64(rep.RPO.Age)
		tr.RPONs = &n
	} else {
		tr.RPOReason = rep.RPO.Reason
		if tr.RPOReason == "" {
			tr.RPOReason = "the canary could not be read"
		}
	}
	tr.RPOStatus = rep.RPOStatus.String()

	rto, terr := measure.ComputeRTO(start, end)
	switch {
	case terr != nil:
		all = append(all, artifact.Result{Check: "rto", Status: artifact.Unverified, Detail: "restore time could not be measured: " + short(terr)})
	case !anyFail(all):
		n := int64(rto)
		tr.RTONs = &n
		tr.RTOStatus = measure.CheckTarget(rto, true, t.RTO.Duration).String()
	}
	return finish()
}

// Decide turns the target results into the gate: 0 all verified and within
// RTO and RPO, 1 degraded, 2 not restorable. Nothing that is not proven good
// counts as good. A drill of no targets is a tool error.
func Decide(ts []TargetResult) Gate {
	if len(ts) == 0 {
		return Gate{Exit: exitcode.ToolError, Decision: "tool_error", Reasons: []string{"no targets were drilled"}}
	}
	g := Gate{Exit: exitcode.OK, Decision: "ok", Reasons: []string{}}
	raise := func(exit int, decision, reason string) {
		if exit > g.Exit {
			g.Exit, g.Decision = exit, decision
		}
		g.Reasons = append(g.Reasons, reason)
	}
	for _, t := range ts {
		switch t.Status {
		case StatusFailed:
			raise(exitcode.NotRestorable, "not_restorable", t.Name+": not restorable, a check failed")
		case StatusUnverified:
			raise(exitcode.Degraded, "degraded", t.Name+": could not be fully verified")
		case StatusVerified:
			switch t.RTOStatus {
			case measure.Within.String():
			case measure.Over.String():
				raise(exitcode.Degraded, "degraded", t.Name+": RTO is over its target")
			default:
				raise(exitcode.Degraded, "degraded", t.Name+": RTO could not be measured")
			}
			switch t.RPOStatus {
			case measure.Within.String():
			case measure.Over.String():
				raise(exitcode.Degraded, "degraded", t.Name+": RPO is over its target")
			default:
				raise(exitcode.Degraded, "degraded", t.Name+": RPO could not be measured")
			}
		default:
			raise(exitcode.Degraded, "degraded", t.Name+": unknown status, treated as not verified")
		}
	}
	return g
}

// Marshal renders the result as indented JSON. The same result always gives the same bytes.
func (r *Result) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Write records the result in dir as drill-<UTC start>.json. An existing file is
// never overwritten: a later result with the same start time gets a -2, -3 suffix.
func (r *Result) Write(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	b, err := r.Marshal()
	if err != nil {
		return "", err
	}
	stamp := "unknown-time"
	if t, perr := time.Parse(time.RFC3339Nano, r.StartedAt); perr == nil {
		stamp = t.UTC().Format("20060102T150405Z")
	}
	for i := 1; i <= 99; i++ {
		name := "drill-" + stamp + ".json"
		if i > 1 {
			name = fmt.Sprintf("drill-%s-%d.json", stamp, i)
		}
		p := filepath.Join(dir, name)
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(b); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return p, nil
	}
	return "", errors.New("too many drill results with the same start time")
}
