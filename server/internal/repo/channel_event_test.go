package repo

import (
	"context"
	"errors"
	"testing"

	"gorm.io/gorm"
)

// TestSanitizeID covers the [A-Za-z0-9_-] allowlist used to build PG
// sequence identifiers (C018 §3.2 §3.4). The function is the *only*
// defence against SQL injection through the dynamic seq name (PG cannot
// parameterise identifiers).
func TestSanitizeID_RejectsInjection(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// allow-listed shapes
		{"ulid uppercase", "01HX5K3QPZWN7MABCDEF12345", "01HX5K3QPZWN7MABCDEF12345"},
		{"uuid hyphenated", "550e8400-e29b-41d4-a716-446655440000", "550e8400-e29b-41d4-a716-446655440000"},
		{"underscore", "ch_test_1", "ch_test_1"},
		{"mixed case + digits", "Abc123XYZ", "Abc123XYZ"},

		// hostile inputs — everything outside [A-Za-z0-9_-] dropped
		{"single quote", "abc'def", "abcdef"},
		{"semicolon injection", "abc;DROP TABLE x", "abcDROPTABLEx"},
		{"comment chain", "abc--def", "abc--def"}, // hyphens kept; `--` is harmless in identifier
		{"comment chain with space", "abc -- def", "abc--def"},
		{"backslash + quote", `a\b'c"d`, "abcd"},
		{"percent", "abc%def", "abcdef"},
		{"dollar quote", "abc$$def", "abcdef"},
		{"unicode", "abc中文def", "abcdef"},
		{"newline", "abc\ndef", "abcdef"},
		{"tab", "abc\tdef", "abcdef"},
		{"null", "abc\x00def", "abcdef"},

		// edge cases
		{"empty", "", ""},
		{"all stripped", `'"";)`, ""},
		{"only hyphens", "----", "----"},
		{"only underscores", "____", "____"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeID(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeID(%q): got %q want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestAppendEvent_RequiresTx asserts the C017 §3.1 rule: AppendEvent with
// tx=nil MUST return ErrTxRequired so callers can't accidentally split the
// business mutation and the event INSERT into two committed transactions.
func TestAppendEvent_RequiresTx(t *testing.T) {
	// nil db is fine — we expect to reject *before* touching the database.
	r := &gormChannelEventRepo{db: nil}

	err := r.AppendEvent(context.Background(), nil, &ChannelEvent{
		ChannelID: "ch1", EventSeq: 1, EventType: EventTypeNew,
		ActorID: "u1",
	})
	if !errors.Is(err, ErrTxRequired) {
		t.Fatalf("expected ErrTxRequired, got %v", err)
	}
}

// TestAppendEvent_NilEventRejected asserts AppendEvent does not panic on a
// nil event pointer (defensive — service-layer composers occasionally pass
// a pointer derived from a struct that may be nil).
func TestAppendEvent_NilEventRejected(t *testing.T) {
	r := &gormChannelEventRepo{db: nil}
	// A non-nil *gorm.DB is needed to bypass the ErrTxRequired short-circuit;
	// the nil-event guard fires before we touch the DB so a zero-value
	// instance is enough.
	tx := &gorm.DB{}
	err := r.AppendEvent(context.Background(), tx, nil)
	if err == nil {
		t.Fatal("expected error for nil event, got nil")
	}
}

// TestEventType_ValueStability locks the wire numbers so a renumbering
// can't sneak through review unnoticed — the cses-client dispatch table
// (Phase P5) keys on these exact integers.
func TestEventType_ValueStability(t *testing.T) {
	cases := map[EventType]int16{
		EventTypeNew:      1,
		EventTypeEdit:     2,
		EventTypeDelete:   3,
		EventTypeReaction: 4,
		EventTypePin:      5,
		EventTypeReadMark: 6,
		EventTypeMember:   7,
	}
	for got, want := range cases {
		if int16(got) != want {
			t.Errorf("EventType wire value drift: got %d want %d", got, want)
		}
	}
}
