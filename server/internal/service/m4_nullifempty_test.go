package service

import "testing"

// TestNullIfEmpty covers the team_id wrapper convention: empty → SQL NULL,
// non-empty → pointer to the value.
func TestNullIfEmpty(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Fatalf("nullIfEmpty(\"\") = %v want nil", got)
	}
	const team = "6111fb0a202d425d221c53db"
	got := nullIfEmpty(team)
	if got == nil || *got != team {
		t.Fatalf("nullIfEmpty(%q) round-trip failed", team)
	}
}
