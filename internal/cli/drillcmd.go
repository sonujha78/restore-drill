package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/sonujha78/restore-drill/internal/adapters"
	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/drill"
	"github.com/sonujha78/restore-drill/internal/exitcode"
)

func fmtNs(n *int64, round time.Duration) string {
	if n == nil {
		return "unknown"
	}
	return time.Duration(*n).Round(round).String()
}

// drillCmd restores and verifies the newest backup of every (selected) target in
// throwaway sandboxes, records the result as JSON, and exits with the gate:
// 0 all verified and within RTO/RPO, 1 degraded, 2 not restorable, 3 tool error.
func drillCmd(args []string, stdout, stderr io.Writer) int {
	fset := flag.NewFlagSet("drill", flag.ContinueOnError)
	fset.SetOutput(stderr)
	cfgPath := fset.String("config", "", "path to the targets YAML (required)")
	targets := fset.String("target", "", "comma separated target names (default: all)")
	outDir := fset.String("out", "out", "directory for the drill result file")
	base := fset.String("sandbox-base", filepath.Join(os.TempDir(), "rdr-drill"), "directory for file based sandboxes")
	if err := fset.Parse(args); err != nil {
		return exitcode.ToolError
	}
	if *cfgPath == "" {
		fmt.Fprintln(stderr, "restore-drill: drill needs --config FILE")
		return exitcode.ToolError
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: %v\n", err)
		return exitcode.ToolError
	}
	var names []string
	for _, n := range strings.Split(*targets, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	res, err := drill.Run(cfg, drill.Options{Targets: names, Clock: clock.Real{}, SandboxBase: *base})
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: %v\n", err)
		return exitcode.ToolError
	}
	path, err := res.Write(*outDir)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: could not record the result: %v\n", err)
		return exitcode.ToolError
	}
	fmt.Fprintf(stdout, "drill started %s\n", res.StartedAt)
	for _, t := range res.Targets {
		fmt.Fprintf(stdout, "  %-16s %-11s rto %s (target %s: %s)  rpo %s (target %s: %s)\n",
			t.Name, t.Status,
			fmtNs(t.RTONs, time.Millisecond), time.Duration(t.RTOTargetNs), t.RTOStatus,
			fmtNs(t.RPONs, time.Second), time.Duration(t.RPOTargetNs), t.RPOStatus)
		for _, r := range t.Reasons {
			fmt.Fprintf(stdout, "      - %s\n", r)
		}
		for _, n := range t.Notes {
			fmt.Fprintf(stdout, "      ! %s\n", n)
		}
	}
	fmt.Fprintf(stdout, "gate: %s (exit %d)\n", res.Gate.Decision, res.Gate.Exit)
	for _, r := range res.Gate.Reasons {
		fmt.Fprintf(stdout, "  - %s\n", r)
	}
	fmt.Fprintf(stdout, "result %s\n", path)
	return res.Gate.Exit
}
