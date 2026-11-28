package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/restore"
	"github.com/sonujha78/restore-drill/internal/sandbox"
	"github.com/sonujha78/restore-drill/internal/verify"
)

// verifyCmd runs the structure, content, canary and application checks on a
// backup that was restored into a sandbox. Exit codes: pass 0, unverified 1,
// fail 2, tool error or refusal 3. --now sets the drill clock (tests, replay).
func verifyCmd(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("verify", flag.ContinueOnError)
	fset.SetOutput(stderr)
	cfgPath := fset.String("config", "", "path to the targets YAML (required)")
	name := fset.String("target", "", "target name (required)")
	sbDir := fset.String("sandbox", "", "sandbox directory the backup was restored into")
	nowStr := fset.String("now", "", "drill clock as RFC 3339 (for tests and replay; default: the real clock)")
	pgdb := fset.String("pgdb", "", "sandbox database the postgres backup was restored into")
	if err := fset.Parse(args); err != nil {
		return exitcode.ToolError
	}
	if *cfgPath == "" || *name == "" {
		fmt.Fprintln(stderr, "restore-drill: verify needs --config FILE --target NAME --sandbox DIR")
		return exitcode.ToolError
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: %v\n", err)
		return exitcode.ToolError
	}
	var tg *config.Target
	for i := range cfg.Targets {
		if cfg.Targets[i].Name == *name {
			tg = &cfg.Targets[i]
		}
	}
	if tg == nil {
		fmt.Fprintf(stderr, "restore-drill: no target named %q in the config\n", *name)
		return exitcode.ToolError
	}
	now := clock.Real{}.Now()
	if *nowStr != "" {
		t, err := time.Parse(time.RFC3339Nano, *nowStr)
		if err != nil {
			fmt.Fprintln(stderr, "restore-drill: --now must be an RFC 3339 time such as 2026-11-27T02:47:30Z")
			return exitcode.ToolError
		}
		now = t
	}

	if tg.Type == "postgres_dump" {
		return verifyPG(tg, *pgdb, now, stdout, stderr)
	}

	if *sbDir == "" {
		fmt.Fprintln(stderr, "restore-drill: verify needs --sandbox DIR")
		return exitcode.ToolError
	}
	sb, err := sandbox.Open(*sbDir)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}

	var src verify.Source
	switch tg.Type {
	case "sqlite":
		p := filepath.Join(sb.Root(), restore.SQLiteFile)
		if _, err := os.Stat(p); errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "restore-drill: no restored database in the sandbox, run restore first")
			return exitcode.ToolError
		}
		s, err := verify.OpenSQLite(p)
		if err != nil {
			fmt.Fprintf(stdout, "target %s (%s)\n  %-11s %-38s %v\noverall: fail\n", tg.Name, tg.Type, artifact.Fail, "open", err)
			return exitcode.NotRestorable
		}
		defer s.Close()
		src = s
	case "tar_gz":
		src = verify.NewFileSource(sb.Root())
	default:
		fmt.Fprintf(stdout, "target %s (%s)\n  %-11s %-38s %s\noverall: unverified\n",
			tg.Name, tg.Type, artifact.Unverified, "verify", "no verify source for this backup type")
		return exitcode.Degraded
	}

	rep := verify.Run(tg, src, now)
	fmt.Fprintf(stdout, "target %s (%s)\nsandbox %s\n", tg.Name, tg.Type, sb.Name())
	for _, r := range rep.Results {
		fmt.Fprintf(stdout, "  %-11s %-38s %s\n", r.Status, r.Check, r.Detail)
	}
	overall := artifact.Overall(rep.Results)
	fmt.Fprintf(stdout, "overall: %s\n", overall)
	if rep.Canary != nil {
		fmt.Fprintf(stdout, "canary %s\n", rep.Canary.UTC().Format(time.RFC3339Nano))
	}
	if rep.RPO.Known {
		fmt.Fprintf(stdout, "rpo    %s (target %s: %s)\n", rep.RPO.Age, tg.RPO.Duration, rep.RPOStatus)
	} else {
		fmt.Fprintf(stdout, "rpo    unknown (%s)\n", rep.RPO.Reason)
	}
	switch overall {
	case artifact.Pass:
		return exitcode.OK
	case artifact.Unverified:
		return exitcode.Degraded
	default:
		return exitcode.NotRestorable
	}
}
