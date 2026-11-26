package measure

import (
	"testing"
	"time"
)

// Deliberately wrong expectation, used once to prove CI blocks merges.
func TestDeliberateFailureForCIProof(t *testing.T) {
	canary := time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)
	now := time.Date(2026, 11, 26, 2, 47, 30, 0, time.UTC)
	got := ComputeRPO(&canary, now)
	if got.Age != 99*time.Minute {
		t.Fatalf("RPO.Age = %v, want 99m (deliberately wrong)", got.Age)
	}
}
