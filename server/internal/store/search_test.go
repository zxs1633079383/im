package store_test

// Integration tests that require a live PG connection.
// Run with:  IM_TEST_PG_DSN=postgres://... go test ./internal/store/... -run TestSearch -v
// They are skipped automatically when IM_TEST_PG_DSN is unset.

import (
	"context"
	"fmt"
	"testing"

	"im-server/internal/model"
	"im-server/internal/store"
	"im-server/internal/testutil"
)

func TestSearchMessages_ReturnsMatchingMessages(t *testing.T) {
	pool := testutil.PGPool(t)
	ss := store.NewSearchStore(pool)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "searcher1", Email: "searcher1@test.com", PasswordHash: "h", DisplayName: "Searcher One"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	ch := &model.Channel{Type: model.ChannelTypeGroup, Name: "search-test-channel"}
	if err := cs.Create(ctx, ch); err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if err := cs.AddMember(ctx, ch.ID, user.ID, model.MemberRoleMember); err != nil {
		t.Fatalf("add member: %v", err)
	}

	msg := &model.Message{
		ChannelID:   ch.ID,
		SenderID:    user.ID,
		ClientMsgID: "search-uuid-001",
		MsgType:     model.MsgTypeText,
		Content:     "hello world search test",
	}
	if err := ms.Send(ctx, msg); err != nil {
		t.Fatalf("send message: %v", err)
	}

	results, err := ss.SearchMessages(ctx, "hello", user.ID, 0, 10)
	if err != nil {
		t.Fatalf("SearchMessages: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	found := false
	for _, r := range results {
		if r.ID == msg.ID {
			found = true
			if r.ChannelName != ch.Name {
				t.Errorf("ChannelName = %q, want %q", r.ChannelName, ch.Name)
			}
		}
	}
	if !found {
		t.Errorf("inserted message not found in search results")
	}
}

func TestSearchMessages_FilterByChannel(t *testing.T) {
	pool := testutil.PGPool(t)
	ss := store.NewSearchStore(pool)
	ms := store.NewMessageStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "searcher2", Email: "searcher2@test.com", PasswordHash: "h", DisplayName: "Searcher Two"}
	if err := us.Create(ctx, user); err != nil {
		t.Fatalf("create user: %v", err)
	}

	ch1 := &model.Channel{Type: model.ChannelTypeGroup, Name: "search-ch1"}
	ch2 := &model.Channel{Type: model.ChannelTypeGroup, Name: "search-ch2"}
	cs.Create(ctx, ch1)
	cs.Create(ctx, ch2)
	cs.AddMember(ctx, ch1.ID, user.ID, model.MemberRoleMember)
	cs.AddMember(ctx, ch2.ID, user.ID, model.MemberRoleMember)

	ms.Send(ctx, &model.Message{ChannelID: ch1.ID, SenderID: user.ID, ClientMsgID: "sc-m1", MsgType: model.MsgTypeText, Content: "unique keyword alpha"})
	ms.Send(ctx, &model.Message{ChannelID: ch2.ID, SenderID: user.ID, ClientMsgID: "sc-m2", MsgType: model.MsgTypeText, Content: "unique keyword beta"})

	results, err := ss.SearchMessages(ctx, "unique", user.ID, ch1.ID, 10)
	if err != nil {
		t.Fatalf("SearchMessages with channelID: %v", err)
	}
	for _, r := range results {
		if r.ChannelID != ch1.ID {
			t.Errorf("got result from channel %d, want only channel %d", r.ChannelID, ch1.ID)
		}
	}
}

func TestSearchUsers_ExcludesCaller(t *testing.T) {
	pool := testutil.PGPool(t)
	ss := store.NewSearchStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	caller := &model.User{Username: "caller_search", Email: "caller_search@test.com", PasswordHash: "h", DisplayName: "Caller Search"}
	target := &model.User{Username: "target_search", Email: "target_search@test.com", PasswordHash: "h", DisplayName: "Target Search"}
	if err := us.Create(ctx, caller); err != nil {
		t.Fatalf("create caller: %v", err)
	}
	if err := us.Create(ctx, target); err != nil {
		t.Fatalf("create target: %v", err)
	}

	results, err := ss.SearchUsers(ctx, "search", caller.ID, 20)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	for _, u := range results {
		if u.ID == caller.ID {
			t.Fatal("caller should be excluded from results")
		}
	}
	found := false
	for _, u := range results {
		if u.ID == target.ID {
			found = true
		}
	}
	if !found {
		t.Error("target user should appear in search results")
	}
}

func TestSearchUsers_MatchesDisplayName(t *testing.T) {
	pool := testutil.PGPool(t)
	ss := store.NewSearchStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	caller := &model.User{Username: fmt.Sprintf("dnCaller%d", 1), Email: "dncaller@test.com", PasswordHash: "h", DisplayName: "Regular Caller"}
	target := &model.User{Username: "dnuser999", Email: "dnuser999@test.com", PasswordHash: "h", DisplayName: "UniqueDisplayXYZ"}
	us.Create(ctx, caller)
	us.Create(ctx, target)

	results, err := ss.SearchUsers(ctx, "UniqueDisplayXYZ", caller.ID, 20)
	if err != nil {
		t.Fatalf("SearchUsers: %v", err)
	}
	found := false
	for _, u := range results {
		if u.ID == target.ID {
			found = true
		}
	}
	if !found {
		t.Error("user with matching display_name should appear in results")
	}
}

func TestSearchChannels_OnlyMemberChannels(t *testing.T) {
	pool := testutil.PGPool(t)
	ss := store.NewSearchStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "chan_searcher", Email: "chansearcher@test.com", PasswordHash: "h", DisplayName: "Chan Searcher"}
	outsider := &model.User{Username: "chan_outsider", Email: "chanoutsider@test.com", PasswordHash: "h", DisplayName: "Chan Outsider"}
	us.Create(ctx, user)
	us.Create(ctx, outsider)

	memberCh := &model.Channel{Type: model.ChannelTypeGroup, Name: "test-member-channel"}
	otherCh := &model.Channel{Type: model.ChannelTypeGroup, Name: "test-other-channel"}
	cs.Create(ctx, memberCh)
	cs.Create(ctx, otherCh)
	cs.AddMember(ctx, memberCh.ID, user.ID, model.MemberRoleMember)
	// user is NOT a member of otherCh

	results, err := ss.SearchChannels(ctx, "test", user.ID, 20)
	if err != nil {
		t.Fatalf("SearchChannels: %v", err)
	}
	for _, ch := range results {
		if ch.ID == otherCh.ID {
			t.Error("channel user is not a member of should not appear in results")
		}
	}
	found := false
	for _, ch := range results {
		if ch.ID == memberCh.ID {
			found = true
		}
	}
	if !found {
		t.Error("member channel should appear in search results")
	}
}

func TestSearchChannels_ExcludesDM(t *testing.T) {
	pool := testutil.PGPool(t)
	ss := store.NewSearchStore(pool)
	cs := store.NewChannelStore(pool)
	us := store.NewUserStore(pool)
	ctx := context.Background()

	user := &model.User{Username: "dm_searcher", Email: "dmsearcher@test.com", PasswordHash: "h", DisplayName: "DM Searcher"}
	us.Create(ctx, user)

	dmCh := &model.Channel{Type: model.ChannelTypeDM, Name: "dm-test-channel"}
	cs.Create(ctx, dmCh)
	cs.AddMember(ctx, dmCh.ID, user.ID, model.MemberRoleMember)

	results, err := ss.SearchChannels(ctx, "dm-test", user.ID, 20)
	if err != nil {
		t.Fatalf("SearchChannels: %v", err)
	}
	for _, ch := range results {
		if ch.ID == dmCh.ID {
			t.Error("DM channel should not appear in channel search results")
		}
	}
}
