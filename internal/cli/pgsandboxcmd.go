package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/pgbox"
)

const pgSandboxUsage = "usage: restore-drill pgsandbox <create|check|remove> --container rdr-NAME [--db NAME]"

// pgSandboxCmd manages throwaway PostgreSQL sandbox databases. Anything that is
// not an rdr- container or not a marked sandbox database is refused (exit 3).
func pgSandboxCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, pgSandboxUsage)
		return exitcode.ToolError
	}
	switch args[0] {
	case "create", "check", "remove":
	default:
		fmt.Fprintf(stderr, "restore-drill: unknown pgsandbox command %q\n%s\n", args[0], pgSandboxUsage)
		return exitcode.ToolError
	}
	fs := flag.NewFlagSet("pgsandbox "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	container := fs.String("container", "", "rdr- container running PostgreSQL (required)")
	db := fs.String("db", "", "sandbox database name (check and remove)")
	if err := fs.Parse(args[1:]); err != nil {
		return exitcode.ToolError
	}
	if *container == "" {
		fmt.Fprintln(stderr, "restore-drill: pgsandbox needs --container NAME")
		return exitcode.ToolError
	}
	if args[0] != "create" && *db == "" {
		fmt.Fprintf(stderr, "restore-drill: pgsandbox %s needs --db NAME\n", args[0])
		return exitcode.ToolError
	}
	runner, err := pgbox.NewDockerRunner(*container)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}
	if args[0] == "create" {
		d, err := pgbox.Create(runner, clock.Real{})
		if err != nil {
			fmt.Fprintf(stderr, "restore-drill: %v\n", err)
			return exitcode.ToolError
		}
		fmt.Fprintln(stdout, d.Name())
		return exitcode.OK
	}
	d, err := pgbox.Open(runner, *db)
	if err != nil {
		fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
		return exitcode.ToolError
	}
	if args[0] == "check" {
		fmt.Fprintf(stdout, "OK: %s is a restore-drill sandbox database\n", d.Name())
		return exitcode.OK
	}
	if err := d.Drop(); err != nil {
		fmt.Fprintf(stderr, "restore-drill: %v\n", err)
		return exitcode.ToolError
	}
	fmt.Fprintf(stdout, "removed %s\n", d.Name())
	return exitcode.OK
}
