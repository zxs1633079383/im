package repo

import "testing"

// TestCollectTopicMembers_M4 covers the dedup + creator-as-owner contract on
// the new string-id pathway. M4: creator and member ids are mm UserIDs.
func TestCollectTopicMembers_M4(t *testing.T) {
	const (
		creator = "676cc4ccfbbc501161d5cd65"
		other1  = "111111111111111111111111"
		other2  = "222222222222222222222222"
	)
	channelID := "01TEST_CHANNEL_99"

	t.Run("creator only — no extra members", func(t *testing.T) {
		got := collectTopicMembers(channelID, creator, nil)
		if len(got) != 1 {
			t.Fatalf("len=%d want 1", len(got))
		}
		if got[0].UserID != creator || got[0].Role != MemberRoleOwner {
			t.Fatalf("expected (creator,owner), got %+v", got[0])
		}
	})

	t.Run("creator listed in members — no duplicate", func(t *testing.T) {
		got := collectTopicMembers(channelID, creator, []string{creator, other1})
		if len(got) != 2 {
			t.Fatalf("len=%d want 2", len(got))
		}
		if got[0].UserID != creator || got[0].Role != MemberRoleOwner {
			t.Fatalf("creator must be first as owner: %+v", got[0])
		}
		if got[1].UserID != other1 || got[1].Role != MemberRoleMember {
			t.Fatalf("second entry must be other1 as member: %+v", got[1])
		}
	})

	t.Run("dup other ids — second occurrence dropped", func(t *testing.T) {
		got := collectTopicMembers(channelID, creator, []string{other1, other2, other1})
		if len(got) != 3 {
			t.Fatalf("len=%d want 3 (creator + 2 distinct)", len(got))
		}
	})

	t.Run("empty member id ignored", func(t *testing.T) {
		got := collectTopicMembers(channelID, creator, []string{"", other1})
		if len(got) != 2 {
			t.Fatalf("len=%d want 2", len(got))
		}
	})
}
