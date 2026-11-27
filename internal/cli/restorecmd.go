package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/pgbox"
	"github.com/sonujha78/restore-drill/internal/restore"
	"github.com/sonujha78/restore-drill/internal/sandbox"
)

// pickBackup returns the chosen file, or the newest match of the target's pattern.
func pickBackup(tg *config.Target, file string) (path string, found bool, err error) {
	if file != "" {
		return file, true, nil
	}
	matches, err := filepath.Glob(filepath.Join(tg.Location.Dir, tg.Location.Pattern))
	if err != nil {
		return "", false, err
	}
	if len(matches) == 0 {
		return "", false, nil
	}
	sort.Strings(matches) // names carry a UTC timestamp, so the last one is the newest
	return matches[len(matches)-1], true, nil
}

// restoreCmd restores the newest (or chosen) backup of a target into a sandbox:
// a directory for file-based types, a throwaway database for postgres_dump.
// Exit codes: 0 restored, 2 could not be restored, 3 tool error or refusal.
func restoreCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the targets YAML (required)")
	name := fs.String("target", "", "target name (required)")
	sbDir := fs.String("sandbox", "", "sandbox directory made by 'sandbox create' (file-based targets)")
	pgdb := fs.String("pgdb", "", "sandbox database made by 'pgsandbox create' (postgres targets)")
	file := fs.String("file", "", "restore this file instead of the newest match")
	if err := fs.Parse(args); err != nil {
		return exitcode.ToolError
	}
	if *cfgPath == "" || *name == "" {
		fmt.Fprintln(stderr, "restore-drill: restore needs --config FILE --target NAME and --sandbox DIR (or --pgdb NAME for postgres)")
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
	if tg.Type == "postgres_dump" {
		return restorePG(tg, *pgdb, *file, stdout, stderr)
	}
	if *sbDir == "" {
		fmt.Fprintln(stderr, "restore-drill: restore needs --sandbox DIR")
		return exitcode.ToolError
	}
	sb, err := sandbox.Open(*sbDir)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}
	path, found, err := pickBackup(tg, *file)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: bad location pattern: %v\n", err)
		return exitcode.ToolError
	}
	if !found {
		fmt.Fprintf(stdout, "target %s: no file matches %s in %s\n", tg.Name, tg.Location.Pattern, tg.Location.Dir)
		return exitcode.NotRestorable
	}
	res, err := restore.Run(tg.Type, path, sb, restore.DefaultLimits(), clock.Real{})
	fmt.Fprintf(stdout, "target %s (%s)\nfile   %s\n", tg.Name, tg.Type, path)
	if err != nil {
		fmt.Fprintf(stdout, "restore FAILED after %d files, %d bytes: %v\n", res.Files, res.Bytes, err)
		return exitcode.NotRestorable
	}
	fmt.Fprintf(stdout, "restored %d files, %d bytes\nrestore time %s\n", res.Files, res.Bytes, res.Duration.Round(1e6))
	return exitcode.OK
}

func restorePG(tg *config.Target, dbName, file string, stdout, stderr io.Writer) int {
	if dbName == "" {
		fmt.Fprintln(stderr, "restore-drill: restoring a postgres target needs --pgdb NAME (made by 'pgsandbox create')")
		return exitcode.ToolError
	}
	runner, err := pgbox.NewDockerRunner(tg.Options["container"])
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}
	db, err := pgbox.Open(runner, dbName)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}
	path, found, err := pickBackup(tg, file)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: bad location pattern: %v\n", err)
		return exitcode.ToolError
	}
	if !found {
		fmt.Fprintf(stdout, "target %s: no file matches %s in %s\n", tg.Name, tg.Location.Pattern, tg.Location.Dir)
		return exitcode.NotRestorable
	}
	clk := clock.Real{}
	start := clk.Now()
	n, err := db.Restore(path, restore.DefaultLimits().MaxBytes)
	dur := clk.Now().Sub(start)
	fmt.Fprintf(stdout, "target %s (%s)\nfile   %s\n", tg.Name, tg.Type, path)
	if err != nil {
		fmt.Fprintf(stdout, "restore FAILED: %v\n", err)
		return exitcode.NotRestorable
	}
	fmt.Fprintf(stdout, "restored into %s, %d bytes read\nrestore time %s\n", db.Name(), n, dur.Round(1e6))
	return exitcode.OK
}
