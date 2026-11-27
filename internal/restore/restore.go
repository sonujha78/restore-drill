// Package restore writes a backup into a sandbox. It can only be handed a
// *sandbox.Sandbox, and every write goes through the sandbox, so a restore
// cannot land anywhere else. Restore time comes from the injected clock.
package restore

import (
	"fmt"
	"sort"
	"time"

	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/sandbox"
)

// Limits stop a decompression bomb. Design values, not tuned on real data.
type Limits struct {
	MaxBytes   int64
	MaxEntries int
}

// DefaultLimits are 256 MiB and 100000 entries.
func DefaultLimits() Limits { return Limits{MaxBytes: 256 << 20, MaxEntries: 100000} }

// Result describes one finished restore.
type Result struct {
	Files    int
	Bytes    int64
	Duration time.Duration
}

// Restorer is the restore step of one adapter.
type Restorer func(src string, sb *sandbox.Sandbox, lim Limits) (Result, error)

var restorers = map[string]Restorer{}

// Register adds the restorer of a backup type. Adapters call it from init().
func Register(kind string, r Restorer) { restorers[kind] = r }

// Kinds lists the registered backup types, sorted.
func Kinds() []string {
	out := make([]string, 0, len(restorers))
	for k := range restorers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Run verifies the sandbox, restores, and measures the time with clk.
func Run(kind, src string, sb *sandbox.Sandbox, lim Limits, clk clock.Clock) (Result, error) {
	if err := sb.Verify(); err != nil {
		return Result{}, err
	}
	r, ok := restorers[kind]
	if !ok {
		return Result{}, fmt.Errorf("no restorer registered for type %q", kind)
	}
	start := clk.Now()
	res, err := r(src, sb, lim)
	res.Duration = clk.Now().Sub(start)
	return res, err
}
