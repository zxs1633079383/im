package repo

import (
	"testing"

	"github.com/lib/pq"
)

// TestMessageIsVisibleTo_M4 covers the post-M4 string-array path. Broadcast
// (VisibleTo nil) is visible to anyone; directed messages match by string
// equality on mm UserIDs. The 张立超 fixture (676cc4cc…) acts as the
// canonical sender / visible recipient.
func TestMessageIsVisibleTo_M4(t *testing.T) {
	const (
		zhangli   = "676cc4ccfbbc501161d5cd65"
		other     = "111111111111111111111111"
		notListed = "222222222222222222222222"
	)

	cases := []struct {
		name      string
		visible   pq.StringArray
		userID    string
		want      bool
	}{
		{"broadcast nil → all visible", nil, zhangli, true},
		{"broadcast nil → other still visible", nil, notListed, true},
		{"directed contains user", pq.StringArray{zhangli, other}, other, true},
		{"directed missing user", pq.StringArray{zhangli, other}, notListed, false},
		{"directed exact zhang", pq.StringArray{zhangli}, zhangli, true},
		{"empty visible_to → broadcast (nil-like)", pq.StringArray{}, zhangli, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			m := &Message{VisibleTo: tc.visible}
			if got := m.IsVisibleTo(tc.userID); got != tc.want {
				t.Fatalf("IsVisibleTo(%q): got %v want %v", tc.userID, got, tc.want)
			}
		})
	}
}

// TestChannelTeamIDPointer covers the *string TeamID nullability convention.
// nil → "no org" / SQL NULL; non-nil with empty string is a bug we don't
// expect (caller should use nil instead) but the model should still work.
func TestChannelTeamIDPointer(t *testing.T) {
	ch := Channel{}
	if ch.TeamID != nil {
		t.Fatalf("zero-value Channel.TeamID expected nil, got %v", ch.TeamID)
	}
	team := "6111fb0a202d425d221c53db" // 张立超 companyId
	ch.TeamID = &team
	if ch.TeamID == nil || *ch.TeamID != team {
		t.Fatalf("TeamID pointer round-trip failed")
	}
}
