// Package measure holds the RTO and RPO maths and the target comparison.
// It never guesses: a value that cannot be measured is reported as unknown.
package measure

import (
	"errors"
	"fmt"
	"time"

	"github.com/sonujha78/restore-drill/internal/clock"
)

// DefaultTolerance is how far a measured duration may sit from a hand-calculated
// one and still count as matching. Design value, not tuned on real drills.
const DefaultTolerance = time.Second

// ErrNegativeDuration means the end of a measurement is before its start.
var ErrNegativeDuration = errors.New("end is before start")

// RPO is the age of the newest data that is really inside a backup.
// Known is false when it cannot be measured, and Reason then says why.
type RPO struct {
	Known  bool
	Age    time.Duration
	Reason string
}

// ComputeRPO returns now minus the canary timestamp. The canary is the marker
// the system wrote before the backup, so this is the age of real data, not of the file.
func ComputeRPO(canary *time.Time, now time.Time) RPO {
	if canary == nil {
		return RPO{Reason: "no canary configured, age of newest data cannot be measured"}
	}
	if canary.IsZero() {
		return RPO{Reason: "canary timestamp is empty, age of newest data cannot be measured"}
	}
	if canary.After(now) {
		return RPO{Reason: fmt.Sprintf(
			"canary timestamp is %s in the future of the drill clock (clock skew or wrong canary)",
			canary.Sub(now))}
	}
	return RPO{Known: true, Age: now.Sub(*canary)}
}

// ComputeRTO returns end minus start. RTO runs from "start restore" to "all checks passed".
func ComputeRTO(start, end time.Time) (time.Duration, error) {
	if end.Before(start) {
		return 0, fmt.Errorf("%w: start=%s end=%s", ErrNegativeDuration,
			start.UTC().Format(time.RFC3339Nano), end.UTC().Format(time.RFC3339Nano))
	}
	return end.Sub(start), nil
}

// Timer measures one restore from start to stop with an injected clock.
type Timer struct {
	clk   clock.Clock
	start time.Time
}

// StartTimer records the start time from clk.
func StartTimer(clk clock.Clock) *Timer {
	return &Timer{clk: clk, start: clk.Now()}
}

// Stop returns the time elapsed since StartTimer.
func (t *Timer) Stop() (time.Duration, error) {
	return ComputeRTO(t.start, t.clk.Now())
}

// Compliance says whether a measured value meets its target.
// The zero value is Unknown on purpose: forgetting to set it can never look like a pass.
type Compliance int

const (
	Unknown Compliance = iota
	Within
	Over
)

func (c Compliance) String() string {
	switch c {
	case Within:
		return "within"
	case Over:
		return "over"
	default:
		return "unknown"
	}
}

// CheckTarget compares a measured duration with its target.
// Exactly at the target is Within. Not measured, or no valid target, is Unknown.
func CheckTarget(measured time.Duration, known bool, target time.Duration) Compliance {
	if !known || target <= 0 || measured < 0 {
		return Unknown
	}
	if measured > target {
		return Over
	}
	return Within
}

// WithinTolerance reports whether |got-want| <= tol (inclusive). A negative tolerance is invalid.
func WithinTolerance(got, want, tol time.Duration) bool {
	if tol < 0 {
		return false
	}
	d := got - want
	if d < 0 {
		d = -d
	}
	return d <= tol
}
