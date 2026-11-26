// Package clock is the only place the tool is allowed to call time.Now().
// Everything else receives a Clock, so drills are replayable and tests are exact.
package clock

import "time"

// Clock is the only way the rest of the tool may ask for the time.
type Clock interface {
	Now() time.Time
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time { return time.Now() }

// Fake is a manually controlled clock for tests and replay.
// It is not safe for concurrent use; drills run checks one after another.
type Fake struct {
	t time.Time
}

func NewFake(t time.Time) *Fake { return &Fake{t: t} }

func (f *Fake) Now() time.Time { return f.t }

func (f *Fake) Set(t time.Time) { f.t = t }

func (f *Fake) Advance(d time.Duration) { f.t = f.t.Add(d) }
