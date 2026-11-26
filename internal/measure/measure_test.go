package measure

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/restore-drill/internal/clock"
)

// Worked examples, calculated by hand BEFORE the code was written:
//   canary 02:00:00, drill clock 02:47:30        -> RPO 47m30s (2850 s)
//   restore start 10:00:00, last check 10:06:40  -> RTO 6m40s  (400 s)

func at(h, m, s int) time.Time {
	return time.Date(2026, 11, 26, h, m, s, 0, time.UTC)
}

// ---------- RPO ----------

func TestRPOWorkedExample(t *testing.T) {
	canary := at(2, 0, 0)
	got := ComputeRPO(&canary, at(2, 47, 30))
	if !got.Known {
		t.Fatalf("RPO.Known = false (reason %q), want true", got.Reason)
	}
	want := 47*time.Minute + 30*time.Second
	if got.Age != want {
		t.Fatalf("RPO.Age = %v, want %v", got.Age, want)
	}
	if got.Age.Seconds() != 2850 {
		t.Fatalf("RPO seconds = %v, want 2850", got.Age.Seconds())
	}
}

func TestRPOAcrossMidnight(t *testing.T) {
	canary := time.Date(2026, 11, 25, 23, 50, 0, 0, time.UTC)
	got := ComputeRPO(&canary, at(0, 10, 0))
	if !got.Known || got.Age != 20*time.Minute {
		t.Fatalf("RPO = %+v, want Known and 20m", got)
	}
}

func TestRPOTimezoneIndependent(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+30*60)
	canary := time.Date(2026, 11, 26, 7, 30, 0, 0, ist) // == 02:00:00 UTC
	got := ComputeRPO(&canary, at(2, 47, 30))
	want := 47*time.Minute + 30*time.Second
	if !got.Known || got.Age != want {
		t.Fatalf("RPO = %+v, want Known and %v", got, want)
	}
}

func TestRPOCanaryEqualsNow(t *testing.T) {
	canary := at(2, 0, 0)
	got := ComputeRPO(&canary, at(2, 0, 0))
	if !got.Known || got.Age != 0 {
		t.Fatalf("RPO = %+v, want Known and 0", got)
	}
}

func TestRPONoCanary(t *testing.T) {
	got := ComputeRPO(nil, at(2, 47, 30))
	if got.Known {
		t.Fatalf("RPO.Known = true with no canary, want false")
	}
	if got.Age != 0 {
		t.Fatalf("RPO.Age = %v with no canary, want 0", got.Age)
	}
	if got.Reason == "" {
		t.Fatalf("RPO.Reason is empty, the report must say why RPO is unknown")
	}
}

func TestRPOZeroCanary(t *testing.T) {
	var zero time.Time
	got := ComputeRPO(&zero, at(2, 47, 30))
	if got.Known {
		t.Fatalf("RPO.Known = true for zero canary time, want false")
	}
	if got.Reason == "" {
		t.Fatalf("RPO.Reason is empty for zero canary time")
	}
}

func TestRPOCanaryInFuture(t *testing.T) {
	canary := at(3, 0, 0)
	got := ComputeRPO(&canary, at(2, 0, 0))
	if got.Known {
		t.Fatalf("RPO.Known = true for a canary from the future, want false")
	}
	if !strings.Contains(got.Reason, "future") {
		t.Fatalf("RPO.Reason = %q, want it to mention the future", got.Reason)
	}
}

// ---------- RTO ----------

func TestRTOWorkedExample(t *testing.T) {
	got, err := ComputeRTO(at(10, 0, 0), at(10, 6, 40))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 400*time.Second {
		t.Fatalf("RTO = %v, want 400s", got)
	}
}

func TestRTOZeroDuration(t *testing.T) {
	got, err := ComputeRTO(at(10, 0, 0), at(10, 0, 0))
	if err != nil || got != 0 {
		t.Fatalf("RTO = %v, err = %v, want 0 and nil", got, err)
	}
}

func TestRTOEndBeforeStart(t *testing.T) {
	_, err := ComputeRTO(at(10, 6, 40), at(10, 0, 0))
	if !errors.Is(err, ErrNegativeDuration) {
		t.Fatalf("err = %v, want ErrNegativeDuration", err)
	}
}

// ---------- Timer ----------

func TestTimerWithFakeClock(t *testing.T) {
	clk := clock.NewFake(at(10, 0, 0))
	tm := StartTimer(clk)
	clk.Advance(400 * time.Second)
	got, err := tm.Stop()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 400*time.Second {
		t.Fatalf("Timer.Stop() = %v, want 400s", got)
	}
}

func TestTimerClockGoesBackwards(t *testing.T) {
	clk := clock.NewFake(at(10, 0, 0))
	tm := StartTimer(clk)
	clk.Set(at(9, 59, 0))
	_, err := tm.Stop()
	if !errors.Is(err, ErrNegativeDuration) {
		t.Fatalf("err = %v, want ErrNegativeDuration", err)
	}
}

// Real clock, real sleep: the measured time must land inside the stated tolerance.
func TestTimerRealClock(t *testing.T) {
	const pause = 300 * time.Millisecond
	tm := StartTimer(clock.Real{})
	time.Sleep(pause)
	got, err := tm.Stop()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got < pause {
		t.Fatalf("measured %v, which is less than the real sleep %v", got, pause)
	}
	if !WithinTolerance(got, pause, DefaultTolerance) {
		t.Fatalf("measured %v, want %v within %v", got, pause, DefaultTolerance)
	}
}

// ---------- Target check ----------

func TestCheckTarget(t *testing.T) {
	cases := []struct {
		name     string
		measured time.Duration
		known    bool
		target   time.Duration
		want     Compliance
	}{
		{"under target", 29*time.Minute + 59*time.Second, true, 30 * time.Minute, Within},
		{"exactly at target is not a breach", 30 * time.Minute, true, 30 * time.Minute, Within},
		{"one nanosecond over", 30*time.Minute + 1, true, 30 * time.Minute, Over},
		{"not measured", 0, false, 30 * time.Minute, Unknown},
		{"no target configured", 10 * time.Minute, true, 0, Unknown},
		{"negative target", 10 * time.Minute, true, -time.Second, Unknown},
		{"negative measured", -time.Second, true, 30 * time.Minute, Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CheckTarget(c.measured, c.known, c.target)
			if got != c.want {
				t.Fatalf("CheckTarget(%v, %v, %v) = %v, want %v",
					c.measured, c.known, c.target, got, c.want)
			}
		})
	}
}

func TestComplianceStringAndZeroValue(t *testing.T) {
	if Within.String() != "within" || Over.String() != "over" || Unknown.String() != "unknown" {
		t.Fatalf("String() = %q/%q/%q, want within/over/unknown",
			Within.String(), Over.String(), Unknown.String())
	}
	var zero Compliance
	if zero != Unknown {
		t.Fatalf("zero value = %v, want Unknown (safe default)", zero)
	}
}

// ---------- Tolerance ----------

func TestWithinTolerance(t *testing.T) {
	want := 400 * time.Second
	cases := []struct {
		name string
		got  time.Duration
		tol  time.Duration
		ok   bool
	}{
		{"exact", 400 * time.Second, time.Second, true},
		{"slightly over", 400*time.Second + 400*time.Millisecond, time.Second, true},
		{"upper boundary is inclusive", 401 * time.Second, time.Second, true},
		{"just past upper boundary", 401*time.Second + time.Millisecond, time.Second, false},
		{"slightly under", 399*time.Second + 500*time.Millisecond, time.Second, true},
		{"lower boundary is inclusive", 399 * time.Second, time.Second, true},
		{"just past lower boundary", 398*time.Second + 999*time.Millisecond, time.Second, false},
		{"negative tolerance is invalid", 400 * time.Second, -time.Second, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := WithinTolerance(c.got, want, c.tol); got != c.ok {
				t.Fatalf("WithinTolerance(%v, %v, %v) = %v, want %v", c.got, want, c.tol, got, c.ok)
			}
		})
	}
}
