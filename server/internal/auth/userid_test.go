package auth

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateUserID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
	}{
		{"real张立超 userId", "676cc4ccfbbc501161d5cd65", true},
		{"real cookieId", "69eec6dbe6876865ff98945a", true},
		{"real companyId", "6111fb0a202d425d221c53db", true},
		{"all zeroes", strings.Repeat("0", 24), true},
		{"all f", strings.Repeat("f", 24), true},
		{"empty", "", false},
		{"too short", "676cc4ccfbbc501161d5cd6", false},
		{"too long", "676cc4ccfbbc501161d5cd650", false},
		{"uppercase A", "676CC4CCFBBC501161D5CD65", false},
		{"non-hex g", strings.Repeat("g", 24), false},
		{"trailing space", "676cc4ccfbbc501161d5cd6 ", false},
		{"leading space", " 76cc4ccfbbc501161d5cd65", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUserID(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !errors.Is(err, ErrInvalidUserID) {
					t.Fatalf("expected ErrInvalidUserID, got %v", err)
				}
			}
		})
	}
}

func TestMustUserID_OK(t *testing.T) {
	got := MustUserID("676cc4ccfbbc501161d5cd65")
	if got != "676cc4ccfbbc501161d5cd65" {
		t.Fatalf("MustUserID returned %q", got)
	}
}

func TestMustUserID_PanicsOnInvalid(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg, _ := r.(string)
		if !strings.Contains(msg, "MustUserID") {
			t.Fatalf("panic msg %q missing call site", msg)
		}
	}()
	_ = MustUserID("nope")
}

func BenchmarkValidateUserID(b *testing.B) {
	id := "676cc4ccfbbc501161d5cd65"
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ValidateUserID(id)
	}
}
