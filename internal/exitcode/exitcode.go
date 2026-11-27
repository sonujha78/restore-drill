// Package exitcode holds the gate contract: what the process exit status means.
package exitcode

const (
	// OK: all targets verified and within RTO/RPO.
	OK = 0
	// Degraded: verified but RTO or RPO over target, or some targets unverified.
	Degraded = 1
	// NotRestorable: at least one backup cannot be restored.
	NotRestorable = 2
	// ToolError: the tool itself failed (bad config, missing file, bug).
	ToolError = 3
)
