package version

import "testing"

func TestStringDefault(t *testing.T) {
	got := String()
	want := "restore-drill dev"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}

func TestStringUsesVersionVariable(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	Version = "v1.2.3"

	got := String()
	want := "restore-drill v1.2.3"
	if got != want {
		t.Fatalf("String() = %q, want %q", got, want)
	}
}
