// Package drill runs one restore drill: artifact checks, a throwaway sandbox,
// a restore with a measured restore time, the verify checks, and cleanup. The
// outcome of every target is verified, unverified or failed, and the whole
// drill is recorded as one JSON result.
package drill

import (
	"github.com/sonujha78/restore-drill/internal/artifact"
	"github.com/sonujha78/restore-drill/internal/clock"
	"github.com/sonujha78/restore-drill/internal/config"
	"github.com/sonujha78/restore-drill/internal/verify"
)

const (
	StatusVerified   = "verified"
	StatusUnverified = "unverified"
	StatusFailed     = "failed"

	// ResultVersion is the version of the JSON result format.
	ResultVersion = 1
)

// Env is what an adapter needs to make a sandbox.
type Env struct {
	Clock       clock.Clock
	SandboxBase string
}

// Session is one restore of one backup into one throwaway sandbox.
type Session interface {
	Restore(file string) error
	Source() (verify.Source, error)
	Close() error
}

// Factory makes a Session. It must refuse anything that is not a sandbox.
type Factory func(t *config.Target, env Env) (Session, error)

// Adapter is everything the drill needs to know about one backup type.
// RestoreProvesReadable says that a successful restore is the proof that the
// backup can be read end to end, so an unverified "readable" artifact check
// may be upgraded after it.
type Adapter struct {
	New                   Factory
	RestoreProvesReadable bool
}

// Options controls one drill.
type Options struct {
	Targets     []string // empty = all targets
	Clock       clock.Clock
	SandboxBase string
	// Artifact overrides the artifact layer (tests). Default: artifact.Run.
	Artifact func(artifact.Params) []artifact.Result
}

// CheckResult is one check outcome, as recorded.
type CheckResult struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

// TargetResult is the recorded outcome of one target.
type TargetResult struct {
	Name         string            `json:"name"`
	Type         string            `json:"type"`
	Status       string            `json:"status"`
	File         string            `json:"file"`
	Checks       []CheckResult     `json:"checks"`
	Reasons      []string          `json:"reasons"`
	CanaryAt     string            `json:"canary_at"`
	RPONs        *int64            `json:"rpo_ns"`
	RPOTargetNs  int64             `json:"rpo_target_ns"`
	RPOStatus    string            `json:"rpo_status"`
	RPOReason    string            `json:"rpo_reason"`
	RTONs        *int64            `json:"rto_ns"`
	RTOTargetNs  int64             `json:"rto_target_ns"`
	RTOStatus    string            `json:"rto_status"`
	SampleHashes map[string]string `json:"sample_hashes"`
	Notes        []string          `json:"notes"`
}

// Gate is the decision for the whole drill.
type Gate struct {
	Exit     int      `json:"exit"`
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
}

// Result is the recorded drill.
type Result struct {
	Version    int            `json:"version"`
	StartedAt  string         `json:"started_at"`
	FinishedAt string         `json:"finished_at"`
	Targets    []TargetResult `json:"targets"`
	Gate       Gate           `json:"gate"`
}
