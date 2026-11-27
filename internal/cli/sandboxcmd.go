package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/sandbox"
)

const sandboxUsage = "usage: restore-drill sandbox <create --base DIR | check --dir DIR | remove --dir DIR>"

// sandboxCmd manages sandboxes. check and remove refuse anything that is not a
// marked sandbox (exit 3).
func sandboxCmd(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, sandboxUsage)
		return exitcode.ToolError
	}
	fs := flag.NewFlagSet("sandbox "+args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	switch args[0] {
	case "create":
		base := fs.String("base", "", "directory to create the sandbox in (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return exitcode.ToolError
		}
		if *base == "" {
			fmt.Fprintln(stderr, "restore-drill: sandbox create needs --base DIR")
			return exitcode.ToolError
		}
		if err := os.MkdirAll(*base, 0o700); err != nil {
			fmt.Fprintf(stderr, "restore-drill: %v\n", err)
			return exitcode.ToolError
		}
		sb, err := sandbox.Create(*base, clock.Real{})
		if err != nil {
			fmt.Fprintf(stderr, "restore-drill: %v\n", err)
			return exitcode.ToolError
		}
		fmt.Fprintln(stdout, sb.Root())
		return exitcode.OK
	case "check", "remove":
		dir := fs.String("dir", "", "directory to check or remove (required)")
		if err := fs.Parse(args[1:]); err != nil {
			return exitcode.ToolError
		}
		if *dir == "" {
			fmt.Fprintf(stderr, "restore-drill: sandbox %s needs --dir DIR\n", args[0])
			return exitcode.ToolError
		}
		sb, err := sandbox.Open(*dir)
		if err != nil {
			fmt.Fprintf(stderr, "restore-drill: refusing: %v\n", err)
			return exitcode.ToolError
		}
		if args[0] == "check" {
			fmt.Fprintf(stdout, "OK: %s is a restore-drill sandbox\n", sb.Name())
			return exitcode.OK
		}
		if err := sb.Remove(); err != nil {
			fmt.Fprintf(stderr, "restore-drill: %v\n", err)
			return exitcode.ToolError
		}
		fmt.Fprintf(stdout, "removed %s\n", sb.Name())
		return exitcode.OK
	default:
		fmt.Fprintf(stderr, "restore-drill: unknown sandbox command %q\n%s\n", args[0], sandboxUsage)
		return exitcode.ToolError
	}
}
