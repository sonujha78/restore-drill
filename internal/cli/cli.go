// Package cli is the command line front end. It is separate from main so it
// can be tested with exit codes and captured output.
package cli

import (
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"sort"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/version"
)

const usage = "usage: restore-drill <version | validate --config FILE | artifact --config FILE --target NAME [--file PATH] [--manifest PATH] | restore --config FILE --target NAME (--sandbox DIR | --pgdb NAME) | pgsandbox <create|check|remove> --container rdr-NAME [--db NAME] | verify --config FILE --target NAME --sandbox DIR [--now RFC3339] | sandbox <create --base DIR | check --dir DIR | remove --dir DIR>>"

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
	case "artifact":
		return artifactCmd(args[1:], stdout, stderr)
	case "sandbox":
		return sandboxCmd(args[1:], stdout, stderr)
	case "restore":
		return restoreCmd(args[1:], stdout, stderr)
	case "pgsandbox":
		return pgSandboxCmd(args[1:], stdout, stderr)
	case "verify":
		return verifyCmd(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "restore-drill: unknown command %q\n%s\n", args[0], usage)
		return exitcode.ToolError
	}
}

// loadConfig checks backup types against the registered adapters, so a new
// type needs no change in the config package.
func loadConfig(path string) (*config.Config, error) {
	return config.Load(path, config.Options{KnownTypes: artifact.Kinds()})
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
	cfg, err := loadConfig(*path)
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

// artifactCmd runs the artifact layer on the newest backup of one target.
// Exit codes: pass 0, unverified 1, fail 2, tool error 3.
func artifactCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("artifact", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "path to the targets YAML (required)")
	name := fs.String("target", "", "target name (required)")
	file := fs.String("file", "", "check this file instead of the newest match")
	manifest := fs.String("manifest", "", "manifest path (default: the target's manifest in its backup dir)")
	if err := fs.Parse(args); err != nil {
		return exitcode.ToolError
	}
	if *cfgPath == "" || *name == "" {
		fmt.Fprintln(stderr, "restore-drill: artifact needs --config FILE and --target NAME")
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

	path := *file
	if path == "" {
		matches, err := filepath.Glob(filepath.Join(tg.Location.Dir, tg.Location.Pattern))
		if err != nil {
			fmt.Fprintf(stderr, "restore-drill: bad location pattern: %v\n", err)
			return exitcode.ToolError
		}
		if len(matches) == 0 {
			fmt.Fprintf(stdout, "target %s (%s)\n  %-11s %-11s no file matches %s in %s\noverall: fail\n",
				tg.Name, tg.Type, artifact.Fail, "exists", tg.Location.Pattern, tg.Location.Dir)
			return exitcode.NotRestorable
		}
		sort.Strings(matches) // names carry a UTC timestamp, so the last one is the newest
		path = matches[len(matches)-1]
	}
	mpath := *manifest
	if mpath == "" && tg.Manifest != "" {
		mpath = filepath.Join(tg.Location.Dir, tg.Manifest)
	}

	results := artifact.Run(artifact.Params{Kind: tg.Type, Path: path, ManifestPath: mpath, MinBytes: tg.MinBytes})
	fmt.Fprintf(stdout, "target %s (%s)\nfile   %s\n", tg.Name, tg.Type, path)
	for _, r := range results {
		fmt.Fprintf(stdout, "  %-11s %-11s %s\n", r.Status, r.Check, r.Detail)
	}
	overall := artifact.Overall(results)
	fmt.Fprintf(stdout, "overall: %s\n", overall)
	switch overall {
	case artifact.Pass:
		return exitcode.OK
	case artifact.Unverified:
		return exitcode.Degraded
	default:
		return exitcode.NotRestorable
	}
}
