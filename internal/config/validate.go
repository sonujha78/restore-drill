package config

import (
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	nameRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)
	envRe   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
)

// safeRelPath accepts only relative paths that stay inside their base after cleaning.
func safeRelPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || filepath.IsAbs(p) || strings.Contains(p, `\`) {
		return false
	}
	c := path.Clean(p)
	return c != "." && c != ".." && !strings.HasPrefix(c, "../")
}

func validate(c *Config, opt Options) []string {
	if len(c.Targets) == 0 {
		return []string{"no targets defined"}
	}
	known := map[string]bool{}
	var knownList []string
	for _, k := range opt.KnownTypes {
		if !known[k] {
			known[k] = true
			knownList = append(knownList, k)
		}
	}
	sort.Strings(knownList)

	var probs []string
	firstSeen := map[string]int{}
	for i := range c.Targets {
		t := &c.Targets[i]
		if nameRe.MatchString(t.Name) {
			if j, dup := firstSeen[t.Name]; dup {
				probs = append(probs, fmt.Sprintf("targets[%d] (%s): duplicate name, already used by targets[%d]", i, t.Name, j))
			} else {
				firstSeen[t.Name] = i
			}
		}
		probs = append(probs, validateTarget(i, t, known, knownList)...)
	}
	return probs
}

func validateTarget(i int, t *Target, known map[string]bool, knownList []string) []string {
	var probs []string
	label := fmt.Sprintf("targets[%d]", i)
	if nameRe.MatchString(t.Name) {
		label += " (" + t.Name + ")"
	}
	add := func(format string, args ...any) {
		probs = append(probs, label+": "+fmt.Sprintf(format, args...))
	}

	switch {
	case t.Name == "":
		add("name is required")
	case !nameRe.MatchString(t.Name):
		add("name must match ^[a-z][a-z0-9-]*$ (lowercase letters, digits and hyphens, at most 63 characters)")
	}

	if t.Type == "" {
		add("type is required")
	} else if len(known) > 0 && !known[t.Type] {
		add("unknown type; known types: %s", strings.Join(knownList, ", "))
	}

	if t.Location.Dir == "" {
		add("location.dir is required")
	}
	if t.Location.Pattern == "" {
		add("location.pattern is required")
	} else if strings.ContainsAny(t.Location.Pattern, `/\`) {
		add("location.pattern must be a file name pattern without path separators")
	}
	if t.Manifest != "" && !safeRelPath(t.Manifest) {
		add("manifest must be a relative path without ..")
	}

	for _, d := range []struct {
		field string
		val   Duration
	}{{"frequency", t.Frequency}, {"rpo", t.RPO}, {"rto", t.RTO}} {
		if d.val.Duration <= 0 {
			add("%s is required and must be greater than 0", d.field)
		}
	}

	if t.Retention.MinCopies < 1 {
		add("retention.min_copies must be at least 1")
	}
	if t.Retention.MaxCopies < t.Retention.MinCopies {
		add("retention.max_copies must be >= retention.min_copies")
	}
	if t.MinBytes < 0 {
		add("min_bytes must be >= 0")
	}

	if cn := t.Canary; cn != nil {
		hasTable := cn.Table != "" || cn.Column != ""
		hasPath := cn.Path != ""
		switch {
		case hasTable && hasPath:
			add("canary: use either table+column or path, not both")
		case !hasTable && !hasPath:
			add("canary: set table+column or path")
		case hasTable:
			if cn.Table == "" || cn.Column == "" {
				add("canary: table and column must be set together")
			} else {
				if !identRe.MatchString(cn.Table) {
					add("canary.table is not a valid identifier")
				}
				if !identRe.MatchString(cn.Column) {
					add("canary.column is not a valid identifier")
				}
			}
		default:
			if !safeRelPath(cn.Path) {
				add("canary.path must be a relative path without ..")
			}
		}
	}

	for ti, tb := range t.Structure.Tables {
		if !identRe.MatchString(tb.Name) {
			add("structure.tables[%d].name is not a valid identifier", ti)
		}
		if len(tb.Columns) == 0 {
			add("structure.tables[%d].columns must not be empty", ti)
		}
		for ci, col := range tb.Columns {
			if !identRe.MatchString(col) {
				add("structure.tables[%d].columns[%d] is not a valid identifier", ti, ci)
			}
		}
	}
	for pi, p := range t.Structure.Paths {
		if !safeRelPath(p) {
			add("structure.paths[%d] must be a relative path without ..", pi)
		}
	}

	for ri, rc := range t.Content.RowCounts {
		if !identRe.MatchString(rc.Table) {
			add("content.row_counts[%d].table is not a valid identifier", ri)
		}
		if rc.Min < 0 {
			add("content.row_counts[%d].min must be >= 0", ri)
		}
		if rc.Max < rc.Min {
			add("content.row_counts[%d].max must be >= min", ri)
		}
	}

	seenCheck := map[string]bool{}
	for ai, ac := range t.AppChecks {
		switch {
		case ac.Name == "":
			add("app_checks[%d].name is required", ai)
		case seenCheck[ac.Name]:
			add("app_checks[%d].name is a duplicate within this target", ai)
		default:
			seenCheck[ac.Name] = true
		}
		if (ac.Query == "") == (ac.Command == "") {
			add("app_checks[%d]: set exactly one of query or command", ai)
		}
		if ac.Expect == "" {
			add("app_checks[%d].expect is required", ai)
		}
	}

	keys := make([]string, 0, len(t.Options))
	for k := range t.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if strings.HasSuffix(strings.ToLower(k), "_env") && !envRe.MatchString(t.Options[k]) {
			add("options.%s must be an environment variable name such as RDR_SOURCE_DSN (value not shown)", k)
		}
	}
	return probs
}
