// Package verify holds the structure, content, canary and application checks.
// It runs after a restore and asks a Source questions about the restored data.
// It only ever reports counts, names and hashes, never row contents. A check
// that cannot run is unverified, never pass.
package verify

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/measure"
)

// SampleRows is how many rows are hashed per table.
const SampleRows = 20

var (
	// ErrUnsupported means this kind of backup cannot answer that question.
	ErrUnsupported = errors.New("not supported by this backup type")
	// ErrNoCanaryData means the canary table or file holds no canary.
	ErrNoCanaryData = errors.New("canary not found")

	numeric = regexp.MustCompile(`^-?[0-9]{1,18}$`)
)

// Source answers questions about one restored backup.
type Source interface {
	Tables() (map[string][]string, error)
	PathExists(rel string) (bool, error)
	RowCount(table string) (int64, error)
	SampleHash(table string, limit int) (hash string, rows int, err error)
	Canary(c config.Canary) (time.Time, error)
	Scalar(query string) (string, error)
	Command(line string) (string, error)
	Integrity() (string, error)
}

// Report is the outcome of all checks of one restored backup.
type Report struct {
	Results      []artifact.Result
	RPO          measure.RPO
	RPOStatus    measure.Compliance
	Canary       *time.Time
	SampleHashes map[string]string
}

type adder func(check string, s artifact.Status, format string, a ...any)

func cut(s string, n int) string {
	s = strings.SplitN(s, "\n", 2)[0]
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func short(err error) string { return cut(err.Error(), 120) }

// Run applies the checks from cheap to expensive.
func Run(t *config.Target, src Source, now time.Time) Report {
	rep := Report{SampleHashes: map[string]string{}}
	add := func(check string, s artifact.Status, format string, a ...any) {
		rep.Results = append(rep.Results, artifact.Result{Check: check, Status: s, Detail: fmt.Sprintf(format, a...)})
	}
	integrity(src, add)
	structure(t, src, add)
	content(t, src, &rep, add)
	canary(t, src, now, &rep, add)
	appChecks(t, src, add)
	return rep
}

func integrity(src Source, add adder) {
	got, err := src.Integrity()
	switch {
	case errors.Is(err, ErrUnsupported):
	case err != nil:
		add("integrity", artifact.Fail, "database could not be read: %s", short(err))
	case got == "ok":
		add("integrity", artifact.Pass, "integrity_check returned ok")
	default:
		add("integrity", artifact.Fail, "integrity_check reported a problem: %s", cut(got, 120))
	}
}

func structure(t *config.Target, src Source, add adder) {
	if len(t.Structure.Tables) > 0 {
		tabs, err := src.Tables()
		lower := map[string][]string{}
		for n, cols := range tabs {
			lower[strings.ToLower(n)] = cols
		}
		for _, spec := range t.Structure.Tables {
			check := "structure:table:" + spec.Name
			switch {
			case errors.Is(err, ErrUnsupported):
				add(check, artifact.Unverified, "this backup type cannot answer table checks")
			case err != nil:
				add(check, artifact.Unverified, "could not run: %s", short(err))
			default:
				cols, ok := lower[strings.ToLower(spec.Name)]
				if !ok {
					add(check, artifact.Fail, "table is missing")
					continue
				}
				have := map[string]bool{}
				for _, c := range cols {
					have[strings.ToLower(c)] = true
				}
				var missing []string
				for _, c := range spec.Columns {
					if !have[strings.ToLower(c)] {
						missing = append(missing, c)
					}
				}
				if len(missing) > 0 {
					add(check, artifact.Fail, "missing columns: %s", strings.Join(missing, ", "))
				} else {
					add(check, artifact.Pass, "table present with all %d expected columns", len(spec.Columns))
				}
			}
		}
	}
	for _, p := range t.Structure.Paths {
		check := "structure:path:" + p
		ok, err := src.PathExists(p)
		switch {
		case errors.Is(err, ErrUnsupported):
			add(check, artifact.Unverified, "this backup type cannot answer path checks")
		case err != nil:
			add(check, artifact.Unverified, "could not run: %s", short(err))
		case !ok:
			add(check, artifact.Fail, "path is missing")
		default:
			add(check, artifact.Pass, "path exists")
		}
	}
}

func content(t *config.Target, src Source, rep *Report, add adder) {
	for _, rc := range t.Content.RowCounts {
		check := "content:rows:" + rc.Table
		n, err := src.RowCount(rc.Table)
		switch {
		case errors.Is(err, ErrUnsupported):
			add(check, artifact.Unverified, "this backup type cannot count rows")
		case err != nil:
			add(check, artifact.Unverified, "could not run: %s", short(err))
		case n < rc.Min || n > rc.Max:
			add(check, artifact.Fail, "%d rows, allowed %d..%d", n, rc.Min, rc.Max)
		default:
			add(check, artifact.Pass, "%d rows (allowed %d..%d)", n, rc.Min, rc.Max)
		}

		hcheck := "content:hash:" + rc.Table
		h, k, err := src.SampleHash(rc.Table, SampleRows)
		switch {
		case errors.Is(err, ErrUnsupported):
			add(hcheck, artifact.Unverified, "this backup type cannot hash rows")
		case err != nil:
			add(hcheck, artifact.Unverified, "could not run: %s", short(err))
		case k == 0:
			add(hcheck, artifact.Unverified, "no rows to sample")
		default:
			rep.SampleHashes[rc.Table] = h
			add(hcheck, artifact.Pass, "sha256 of the first %d rows = %s... (recorded as evidence, not compared with a known value)", k, h[:12])
		}
	}
}

func canary(t *config.Target, src Source, now time.Time, rep *Report, add adder) {
	if t.Canary == nil {
		rep.RPO = measure.ComputeRPO(nil, now)
		rep.RPOStatus = measure.CheckTarget(rep.RPO.Age, rep.RPO.Known, t.RPO.Duration)
		add("canary", artifact.Unverified, "no canary configured, RPO cannot be measured")
		return
	}
	ts, err := src.Canary(*t.Canary)
	switch {
	case errors.Is(err, ErrUnsupported):
		add("canary", artifact.Unverified, "this backup type cannot read that canary")
	case errors.Is(err, ErrNoCanaryData):
		add("canary", artifact.Fail, "no canary row found in the backup (empty or wrong data)")
	case err != nil:
		add("canary", artifact.Unverified, "could not read the canary: %s", short(err))
	default:
		rep.Canary = &ts
		rep.RPO = measure.ComputeRPO(&ts, now)
		if !rep.RPO.Known {
			add("canary", artifact.Unverified, "%s", rep.RPO.Reason)
		} else {
			add("canary", artifact.Pass, "newest canary %s, age %s", ts.UTC().Format(time.RFC3339), rep.RPO.Age)
		}
	}
	rep.RPOStatus = measure.CheckTarget(rep.RPO.Age, rep.RPO.Known, t.RPO.Duration)
}

func shown(v string) string {
	if numeric.MatchString(v) {
		return v
	}
	return "<non-numeric value withheld>"
}

func appChecks(t *config.Target, src Source, add adder) {
	for _, ac := range t.AppChecks {
		check := "app:" + ac.Name
		var v string
		var err error
		if ac.Query != "" {
			v, err = src.Scalar(ac.Query)
		} else {
			v, err = src.Command(ac.Command)
		}
		switch {
		case errors.Is(err, ErrUnsupported):
			add(check, artifact.Unverified, "this backup type cannot run that kind of check")
		case err != nil:
			add(check, artifact.Unverified, "could not run: %s", short(err))
		case strings.TrimSpace(v) != ac.Expect:
			add(check, artifact.Fail, "returned %s, expected %s", shown(strings.TrimSpace(v)), ac.Expect)
		default:
			add(check, artifact.Pass, "returned the expected result %s", ac.Expect)
		}
	}
}
