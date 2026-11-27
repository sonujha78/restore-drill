// Package cli is the command line front end. It is separate from main so it
// can be tested with exit codes and captured output.
package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/version"
)

const usage = "usage: restore-drill <version | validate --config FILE>"

// Run executes one command and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitcode.ToolError
	}
	switch args[0] {
	case "version", "--version":
		fmt.Fprintln(stdout, version.String())
		return exitcode.OK
	case "validate":
		return validate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "restore-drill: unknown command %q\n%s\n", args[0], usage)
		return exitcode.ToolError
	}
}

func validate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("config", "", "path to the targets YAML (required)")
	if err := fs.Parse(args); err != nil {
		return exitcode.ToolError
	}
	if *path == "" {
		fmt.Fprintln(stderr, "restore-drill: validate needs --config FILE")
		return exitcode.ToolError
	}
	// Adapter types are not registered yet, so the type is not checked here.
	cfg, err := config.Load(*path, config.Options{})
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: %v\n", err)
		return exitcode.ToolError
	}
	fmt.Fprintf(stdout, "OK: %d targets valid\n", len(cfg.Targets))
	for _, t := range cfg.Targets {
		fmt.Fprintf(stdout, "  %s (%s)\n", t.Name, t.Type)
	}
	return exitcode.OK
}
