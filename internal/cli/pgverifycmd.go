package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/exitcode"
	"github.com/sonujha78/restore-drill/internal/pgbox"
	"github.com/sonujha78/restore-drill/internal/verify"
)

// verifyPG verifies a postgres target that was restored into a sandbox database.
// Only rdr- containers and marked sandbox databases are accepted (exit 3 otherwise).
func verifyPG(tg *config.Target, dbName string, now time.Time, stdout, stderr io.Writer) int {
	if dbName == "" {
		fmt.Fprintln(stderr, "restore-drill: verifying a postgres target needs --pgdb NAME (the sandbox database it was restored into)")
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
	rep := verify.Run(tg, verify.NewPGSource(db), now)
	fmt.Fprintf(stdout, "target %s (%s)\ndatabase %s\n", tg.Name, tg.Type, db.Name())
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
