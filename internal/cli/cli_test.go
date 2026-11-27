package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const okYAML = `targets:
  - name: t1
    type: sqlite
    location: {dir: /b, pattern: "t-*.db"}
    frequency: 1h
    retention: {min_copies: 1, max_copies: 3}
    rpo: 2h
    rto: 60s
`

func run(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "targets.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestVersion(t *testing.T) {
	code, out, _ := run("version")
	if code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !strings.Contains(out, "restore-drill") {
		t.Fatalf("stdout = %q, want the version banner", out)
	}
}

func TestNoArgsIsToolError(t *testing.T) {
	code, _, errOut := run()
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if !strings.Contains(errOut, "usage") {
		t.Fatalf("stderr = %q, want a usage line", errOut)
	}
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := run("frobnicate")
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("stderr = %q, want 'unknown command'", errOut)
	}
}

func TestValidateOK(t *testing.T) {
	code, out, errOut := run("validate", "--config", writeTemp(t, okYAML))
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "OK: 1 targets valid") || !strings.Contains(out, "t1 (sqlite)") {
		t.Fatalf("stdout = %q, want the OK line and the target", out)
	}
}

func TestValidateNeedsConfigFlag(t *testing.T) {
	code, _, errOut := run("validate")
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if !strings.Contains(errOut, "--config") {
		t.Fatalf("stderr = %q, want a hint about --config", errOut)
	}
}

func TestValidateMissingFile(t *testing.T) {
	code, _, _ := run("validate", "--config", filepath.Join(t.TempDir(), "nope.yaml"))
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
}

func TestValidateBadConfigIsToolErrorAndNeverLeaks(t *testing.T) {
	bad := okYAML + "    password: hunter2\n"
	code, out, errOut := run("validate", "--config", writeTemp(t, bad))
	if code != 3 {
		t.Fatalf("exit = %d, want 3", code)
	}
	if !strings.Contains(errOut, "looks like a secret") {
		t.Fatalf("stderr = %q, want the secret warning", errOut)
	}
	if strings.Contains(out+errOut, "hunter2") {
		t.Fatalf("secret leaked in output: stdout=%q stderr=%q", out, errOut)
	}
}

func TestValidateRealDemoConfig(t *testing.T) {
	code, out, errOut := run("validate", "--config", "../../configs/targets.yaml")
	if code != 0 {
		t.Fatalf("exit = %d, want 0 (stderr: %s)", code, errOut)
	}
	if !strings.Contains(out, "OK: 4 targets valid") {
		t.Fatalf("stdout = %q, want 'OK: 4 targets valid'", out)
	}
}
