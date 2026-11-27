package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const baseYAML = `targets:
  - name: pg-orders
    type: postgres_dump
    location:
      dir: /backups/pg-orders
      pattern: "orders-*.dump"
    manifest: SHA256SUMS
    frequency: 1h
    retention: {min_copies: 3, max_copies: 7}
    rpo: 2h
    rto: 60s
    min_bytes: 2048
    canary: {table: rdr_canary, column: written_at}
    structure:
      tables:
        - {name: orders, columns: [id, customer_id, amount]}
      paths: [data/a.txt]
    content:
      row_counts:
        - {table: orders, min: 200, max: 50000}
    app_checks:
      - name: no-negative-amounts
        query: "SELECT count(*) FROM orders WHERE amount < 0"
        expect: "0"
    options:
      container: rdr-pg
      dsn_env: RDR_RESTORE_DSN
`

// mutate replaces old with repl in doc. old must occur exactly once, so a typo
// in a test can never turn it into a silent no-op.
func mutate(t *testing.T, doc, old, repl string) string {
	t.Helper()
	if n := strings.Count(doc, old); n != 1 {
		t.Fatalf("test bug: %q must occur exactly once in the document, found %d", old, n)
	}
	return strings.Replace(doc, old, repl, 1)
}

func parseOK(t *testing.T, doc string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(doc), Options{})
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}
	return cfg
}

func parseFail(t *testing.T, doc string, opt Options, want string) error {
	t.Helper()
	cfg, err := Parse([]byte(doc), opt)
	if err == nil {
		t.Fatalf("Parse() returned no error (%d targets), want error containing %q", len(cfg.Targets), want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Parse() error = %q, want it to contain %q", err.Error(), want)
	}
	return err
}

func eq(t *testing.T, field string, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", field, got, want)
	}
}

func TestParseValid(t *testing.T) {
	cfg := parseOK(t, baseYAML)
	if len(cfg.Targets) != 1 {
		t.Fatalf("len(Targets) = %d, want 1", len(cfg.Targets))
	}
	tg := cfg.Targets[0]
	eq(t, "Name", tg.Name, "pg-orders")
	eq(t, "Type", tg.Type, "postgres_dump")
	eq(t, "Location.Dir", tg.Location.Dir, "/backups/pg-orders")
	eq(t, "Location.Pattern", tg.Location.Pattern, "orders-*.dump")
	eq(t, "Manifest", tg.Manifest, "SHA256SUMS")
	eq(t, "Frequency", tg.Frequency.Duration, time.Hour)
	eq(t, "RPO", tg.RPO.Duration, 2*time.Hour)
	eq(t, "RTO", tg.RTO.Duration, 60*time.Second)
	eq(t, "Retention.MinCopies", tg.Retention.MinCopies, 3)
	eq(t, "Retention.MaxCopies", tg.Retention.MaxCopies, 7)
	eq(t, "MinBytes", tg.MinBytes, int64(2048))
	if tg.Canary == nil {
		t.Fatalf("Canary = nil, want table canary")
	}
	eq(t, "Canary.Table", tg.Canary.Table, "rdr_canary")
	eq(t, "Canary.Column", tg.Canary.Column, "written_at")
	if len(tg.Structure.Tables) != 1 || len(tg.Structure.Tables[0].Columns) != 3 {
		t.Fatalf("Structure.Tables = %+v, want 1 table with 3 columns", tg.Structure.Tables)
	}
	if len(tg.Content.RowCounts) != 1 {
		t.Fatalf("len(RowCounts) = %d, want 1", len(tg.Content.RowCounts))
	}
	eq(t, "RowCounts[0].Min", tg.Content.RowCounts[0].Min, int64(200))
	eq(t, "RowCounts[0].Max", tg.Content.RowCounts[0].Max, int64(50000))
	if len(tg.AppChecks) != 1 {
		t.Fatalf("len(AppChecks) = %d, want 1", len(tg.AppChecks))
	}
	eq(t, "AppChecks[0].Expect", tg.AppChecks[0].Expect, "0")
	eq(t, "Options[container]", tg.Options["container"], "rdr-pg")
}

func TestRequiredAndWrongValues(t *testing.T) {
	cases := []struct{ name, old, repl, want string }{
		{"name missing", "name: pg-orders", `name: ""`, "name is required"},
		{"name has uppercase and space", "name: pg-orders", `name: "Pg Orders"`, "name must match"},
		{"name starts with hyphen", "name: pg-orders", `name: "-x"`, "name must match"},
		{"type missing", "type: postgres_dump", `type: ""`, "type is required"},
		{"dir missing", "      dir: /backups/pg-orders\n", "", "location.dir is required"},
		{"pattern missing", "      pattern: \"orders-*.dump\"\n", "", "location.pattern is required"},
		{"pattern has slash", `pattern: "orders-*.dump"`, `pattern: "sub/orders-*.dump"`, "location.pattern must be a file name pattern"},
		{"frequency missing", "    frequency: 1h\n", "", "frequency is required"},
		{"rpo missing", "    rpo: 2h\n", "", "rpo is required"},
		{"rto missing", "    rto: 60s\n", "", "rto is required"},
		{"rto in days", "rto: 60s", "rto: 1d", "invalid duration"},
		{"rto not a duration", "rto: 60s", "rto: abc", "invalid duration"},
		{"rto without unit", "rto: 60s", "rto: 60", "invalid duration"},
		{"rto is a list", "rto: 60s", "rto: [60]", "duration must be"},
		{"rto negative", "rto: 60s", "rto: -5m", "must be greater than 0"},
		{"rto zero", "rto: 60s", "rto: 0s", "must be greater than 0"},
		{"retention missing", "    retention: {min_copies: 3, max_copies: 7}\n", "", "retention.min_copies must be at least 1"},
		{"min_copies zero", "min_copies: 3, max_copies: 7", "min_copies: 0, max_copies: 7", "retention.min_copies must be at least 1"},
		{"max below min", "min_copies: 3, max_copies: 7", "min_copies: 5, max_copies: 2", "retention.max_copies must be >= retention.min_copies"},
		{"min_bytes negative", "min_bytes: 2048", "min_bytes: -1", "min_bytes must be >= 0"},
		{"manifest escapes", "manifest: SHA256SUMS", "manifest: ../SHA256SUMS", "manifest must be a relative path"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parseFail(t, mutate(t, baseYAML, c.old, c.repl), Options{}, c.want)
		})
	}
}

func TestDuplicateTargetNames(t *testing.T) {
	doc := baseYAML + strings.TrimPrefix(baseYAML, "targets:\n")
	parseFail(t, doc, Options{}, "duplicate name")
}

func TestUnknownFieldsRejected(t *testing.T) {
	parseFail(t, mutate(t, baseYAML, "    min_bytes: 2048\n", "    min_bytes: 2048\n    retries: 3\n"), Options{}, "retries")
	parseFail(t, "foo: 1\n"+baseYAML, Options{}, "foo")
}

func TestKnownTypesComeFromTheRegistry(t *testing.T) {
	parseFail(t, baseYAML, Options{KnownTypes: []string{"tar_gz", "sqlite"}}, "unknown type; known types: sqlite, tar_gz")
	if _, err := Parse([]byte(baseYAML), Options{KnownTypes: []string{"postgres_dump"}}); err != nil {
		t.Fatalf("registered type rejected: %v", err)
	}
	if _, err := Parse([]byte(baseYAML), Options{}); err != nil {
		t.Fatalf("no registry given, type must not be checked: %v", err)
	}
}

func TestCanaryRules(t *testing.T) {
	const canary = "    canary: {table: rdr_canary, column: written_at}\n"

	cfg := parseOK(t, mutate(t, baseYAML, canary, ""))
	if cfg.Targets[0].Canary != nil {
		t.Fatalf("Canary = %+v, want nil when not configured", cfg.Targets[0].Canary)
	}

	parseFail(t, mutate(t, baseYAML, canary, "    canary: {table: rdr_canary, column: written_at, path: x/y}\n"), Options{}, "not both")
	parseFail(t, mutate(t, baseYAML, canary, "    canary: {}\n"), Options{}, "set table+column or path")
	parseFail(t, mutate(t, baseYAML, canary, "    canary: {table: rdr_canary}\n"), Options{}, "set together")
	parseFail(t, mutate(t, baseYAML, canary, "    canary: {table: \"a b\", column: written_at}\n"), Options{}, "canary.table is not a valid identifier")
	parseFail(t, mutate(t, baseYAML, canary, "    canary: {path: ../x}\n"), Options{}, "canary.path must be a relative path")

	cfg = parseOK(t, mutate(t, baseYAML, canary, "    canary: {path: data/canary.txt}\n"))
	eq(t, "Canary.Path", cfg.Targets[0].Canary.Path, "data/canary.txt")
}

func TestUnsafePathsRejected(t *testing.T) {
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "data/../../x"} {
		t.Run(p, func(t *testing.T) {
			parseFail(t, mutate(t, baseYAML, "paths: [data/a.txt]", "paths: ["+p+"]"), Options{}, "structure.paths[0] must be a relative path")
		})
	}
	parseOK(t, mutate(t, baseYAML, "paths: [data/a.txt]", "paths: [a/b/../c.txt]"))
}

func TestIdentifiersAreChecked(t *testing.T) {
	const tbl = "{name: orders, columns: [id, customer_id, amount]}"
	parseFail(t, mutate(t, baseYAML, tbl, `{name: "orders; DROP TABLE x", columns: [id]}`), Options{}, "structure.tables[0].name is not a valid identifier")
	parseFail(t, mutate(t, baseYAML, tbl, `{name: orders, columns: [id, "bad col"]}`), Options{}, "structure.tables[0].columns[1] is not a valid identifier")
	parseFail(t, mutate(t, baseYAML, tbl, "{name: orders, columns: []}"), Options{}, "structure.tables[0].columns must not be empty")
	parseFail(t, mutate(t, baseYAML, "{table: orders, min: 200, max: 50000}", `{table: "x y", min: 1, max: 2}`), Options{}, "content.row_counts[0].table is not a valid identifier")
}

func TestRowCountRange(t *testing.T) {
	const rc = "{table: orders, min: 200, max: 50000}"
	parseFail(t, mutate(t, baseYAML, rc, "{table: orders, min: 500, max: 100}"), Options{}, "content.row_counts[0].max must be >= min")
	parseFail(t, mutate(t, baseYAML, rc, "{table: orders, min: -1, max: 5}"), Options{}, "content.row_counts[0].min must be >= 0")
	parseOK(t, mutate(t, baseYAML, rc, "{table: orders, min: 0, max: 0}"))
}

func TestAppCheckRules(t *testing.T) {
	parseFail(t, mutate(t, baseYAML, "        expect: \"0\"\n", "        command: \"true\"\n        expect: \"0\"\n"), Options{}, "exactly one of query or command")
	parseFail(t, mutate(t, baseYAML, "        query: \"SELECT count(*) FROM orders WHERE amount < 0\"\n", ""), Options{}, "exactly one of query or command")
	parseFail(t, mutate(t, baseYAML, "        expect: \"0\"\n", ""), Options{}, "app_checks[0].expect is required")
	parseFail(t, mutate(t, baseYAML, "- name: no-negative-amounts", `- name: ""`), Options{}, "app_checks[0].name is required")
}

// The secret tests all use the marker "hunter2". It must never appear in an
// error message, whatever the field.
func TestSecretsAreRejectedAndNeverEchoed(t *testing.T) {
	const anchor = "      container: rdr-pg\n"
	cases := []struct{ name, doc, want string }{
		{"password key", mutate(t, baseYAML, anchor, anchor+"      password: hunter2\n"), "looks like a secret"},
		{"connection_string key", mutate(t, baseYAML, anchor, anchor+"      connection_string: x-hunter2-x\n"), "looks like a secret"},
		{"dsn key", mutate(t, baseYAML, anchor, anchor+"      dsn: postgres://app:hunter2@db/orders\n"), "looks like a secret"},
		{"key contains password", mutate(t, baseYAML, anchor, anchor+"      db_password: hunter2\n"), "looks like a secret"},
		{"password at target level", mutate(t, baseYAML, "    rto: 60s\n", "    rto: 60s\n    password: hunter2\n"), "looks like a secret"},
		{"url with credentials", mutate(t, baseYAML, anchor, anchor+"      note: postgres://app:hunter2@db/orders\n"), "looks like a credential"},
		{"password= in a value", mutate(t, baseYAML, anchor, anchor+"      note: \"host=db user=app password=hunter2\"\n"), "looks like a credential"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := parseFail(t, c.doc, Options{}, c.want)
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("secret leaked in error message: %q", err.Error())
			}
		})
	}
}

func TestEnvNameKeysAreTheAllowedWay(t *testing.T) {
	const anchor = "      container: rdr-pg\n"
	cfg := parseOK(t, mutate(t, baseYAML, anchor, anchor+"      db_password_env: RDR_DB_PASSWORD\n"))
	eq(t, "Options[db_password_env]", cfg.Targets[0].Options["db_password_env"], "RDR_DB_PASSWORD")

	err := parseFail(t, mutate(t, baseYAML, "dsn_env: RDR_RESTORE_DSN", "dsn_env: hunter2"), Options{}, "options.dsn_env must be an environment variable name")
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("secret leaked in error message: %q", err.Error())
	}
}

// yaml.v3 puts the offending value into its type errors. We must redact it.
func TestTypeErrorDoesNotLeakTheValue(t *testing.T) {
	err := parseFail(t, mutate(t, baseYAML, "min_bytes: 2048", "min_bytes: hunter2"), Options{}, "cannot unmarshal")
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("value leaked in type error: %q", err.Error())
	}
}

func TestEmptyAndBrokenInput(t *testing.T) {
	parseFail(t, "", Options{}, "empty")
	parseFail(t, "  \n\n", Options{}, "empty")
	parseFail(t, "# only a comment\n", Options{}, "empty")
	parseFail(t, "targets: []\n", Options{}, "no targets")
	parseFail(t, "targets: [\n", Options{}, "not valid YAML")
}

func TestAllProblemsAreReportedTogetherAndDeterministically(t *testing.T) {
	doc := mutate(t, baseYAML, "    rto: 60s\n", "")
	doc = mutate(t, doc, "    rpo: 2h\n", "")
	doc = mutate(t, doc, "    frequency: 1h\n", "")

	_, err1 := Parse([]byte(doc), Options{})
	_, err2 := Parse([]byte(doc), Options{})
	var ve *ValidationError
	if !errors.As(err1, &ve) {
		t.Fatalf("error type = %T, want *ValidationError", err1)
	}
	if len(ve.Problems) != 3 {
		t.Fatalf("len(Problems) = %d, want 3: %v", len(ve.Problems), ve.Problems)
	}
	if err2 == nil || err1.Error() != err2.Error() {
		t.Fatalf("two parses of the same input gave different errors:\n%v\n%v", err1, err2)
	}
}

func TestRealDemoConfig(t *testing.T) {
	cfg, err := Load("../../configs/targets.yaml", Options{})
	if err != nil {
		t.Fatalf("Load(demo config) error: %v", err)
	}
	want := []string{"pg-orders", "sqlite-notes", "files-tar", "sqlite-nocanary"}
	if len(cfg.Targets) != len(want) {
		t.Fatalf("len(Targets) = %d, want %d", len(cfg.Targets), len(want))
	}
	noCanary := 0
	for i, tg := range cfg.Targets {
		if tg.Name != want[i] {
			t.Errorf("Targets[%d].Name = %q, want %q", i, tg.Name, want[i])
		}
		if tg.Canary == nil {
			noCanary++
			if tg.Name != "sqlite-nocanary" {
				t.Errorf("target %q has no canary, only sqlite-nocanary may", tg.Name)
			}
		}
	}
	if noCanary != 1 {
		t.Errorf("targets without canary = %d, want exactly 1", noCanary)
	}
}
