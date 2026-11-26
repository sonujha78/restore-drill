package clock

import (
	"testing"
	"time"
)

func t0() time.Time {
	return time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
}

func TestFakeNowReturnsStart(t *testing.T) {
	f := NewFake(t0())
	if !f.Now().Equal(t0()) {
		t.Fatalf("Now() = %v, want %v", f.Now(), t0())
	}
}

func TestFakeAdvance(t *testing.T) {
	f := NewFake(t0())
	f.Advance(400 * time.Second)
	want := time.Date(2026, 11, 26, 10, 6, 40, 0, time.UTC)
	if !f.Now().Equal(want) {
		t.Fatalf("after Advance(400s) Now() = %v, want %v", f.Now(), want)
	}
}

func TestFakeSet(t *testing.T) {
	f := NewFake(t0())
	target := time.Date(2026, 11, 27, 1, 2, 3, 0, time.UTC)
	f.Set(target)
	if !f.Now().Equal(target) {
		t.Fatalf("after Set Now() = %v, want %v", f.Now(), target)
	}
}

func TestRealClockMovesForward(t *testing.T) {
	var c Clock = Real{}
	a := c.Now()
	time.Sleep(5 * time.Millisecond)
	b := c.Now()
	if !b.After(a) {
		t.Fatalf("real clock did not move forward: a=%v b=%v", a, b)
	}
}
