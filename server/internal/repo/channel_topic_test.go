package repo

import (
	"testing"
)

func TestCollectTopicMembers_DedupesAndPrependsCreator(t *testing.T) {
	got := collectTopicMembers(99, 1, []int64{2, 3, 1, 2})
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (creator + 2 unique members)", len(got))
	}
	// Creator must come first with Owner role; dedup keeps first occurrence.
	if got[0].UserID != 1 || got[0].Role != MemberRoleOwner {
		t.Errorf("got[0] = %+v, want creator as owner", got[0])
	}
	for i, want := range []int64{1, 2, 3} {
		if got[i].UserID != want {
			t.Errorf("got[%d].UserID = %d, want %d", i, got[i].UserID, want)
		}
		if got[i].ChannelID != 99 {
			t.Errorf("got[%d].ChannelID = %d, want 99", i, got[i].ChannelID)
		}
	}
	if got[1].Role != MemberRoleMember || got[2].Role != MemberRoleMember {
		t.Errorf("non-creator members must be plain Members, got %+v", got[1:])
	}
}

func TestCollectTopicMembers_OnlyCreator(t *testing.T) {
	got := collectTopicMembers(7, 42, nil)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].UserID != 42 || got[0].Role != MemberRoleOwner {
		t.Errorf("got[0] = %+v, want creator as owner", got[0])
	}
}

func TestCollectTopicMembers_CreatorInMemberListNotDuplicated(t *testing.T) {
	got := collectTopicMembers(7, 42, []int64{42, 43})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (creator must not be duplicated)", len(got))
	}
	if got[0].UserID != 42 || got[0].Role != MemberRoleOwner {
		t.Errorf("got[0] = %+v, want creator/owner first", got[0])
	}
	if got[1].UserID != 43 || got[1].Role != MemberRoleMember {
		t.Errorf("got[1] = %+v, want 43 as member", got[1])
	}
}
