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
	"github.com/sonujha78/restore-drill/internal/restore"
	"github.com/sonujha78/restore-drill/internal/sandbox"
)

// restoreCmd restores the newest (or chosen) backup of a target into a sandbox.
// Exit codes: 0 restored, 2 could not be restored, 3 tool error or refusal.
func restoreCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restore", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the targets YAML (required)")
	name := fs.String("target", "", "target name (required)")
	sbDir := fs.String("sandbox", "", "sandbox directory made by 'sandbox create' (required)")
	file := fs.String("file", "", "restore this file instead of the newest match")
	if err := fs.Parse(args); err != nil {
		return exitcode.ToolError
	}
	if *cfgPath == "" || *name == "" || *sbDir == "" {
		fmt.Fprintln(stderr, "restore-drill: restore needs --config FILE --target NAME --sandbox DIR")
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
	sb, err := sandbox.Open(*sbDir)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}
	path := *file
	if path == "" {
		matches, err := filepath.Glob(filepath.Join(tg.Location.Dir, tg.Location.Pattern))
		if err != nil {
			fmt.Fprintf(stderr, "restore-drill: bad location pattern: %v\n", err)
			return exitcode.ToolError
		}
		if len(matches) == 0 {
			fmt.Fprintf(stdout, "target %s: no file matches %s in %s\n", tg.Name, tg.Location.Pattern, tg.Location.Dir)
			return exitcode.NotRestorable
		}
		sort.Strings(matches)
		path = matches[len(matches)-1]
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
