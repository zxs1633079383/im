package handler_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/handler"
	"im-server/internal/repo"
)

// ---------- in-memory stub ChannelStore ----------

type stubChannelStore struct {
	channels []repo.Channel
	members  []repo.ChannelMember
	nextID   int64
}

func newStubChannelStore() *stubChannelStore {
	return &stubChannelStore{nextID: 1}
}

func (s *stubChannelStore) Create(_ context.Context, ch *repo.Channel) error {
	ch.ID = s.nextID
	s.nextID++
	s.channels = append(s.channels, *ch)
	return nil
}

func (s *stubChannelStore) GetByID(_ context.Context, id int64) (*repo.Channel, error) {
	for i := range s.channels {
		if s.channels[i].ID == id {
			c := s.channels[i]
			return &c, nil
		}
	}
	return nil, handler.ErrNotFound
}

func (s *stubChannelStore) Update(_ context.Context, channelID int64, name, avatarURL string) error {
	for i := range s.channels {
		if s.channels[i].ID == channelID {
			if name != "" {
				s.channels[i].Name = name
			}
			if avatarURL != "" {
				s.channels[i].AvatarURL = avatarURL
			}
			return nil
		}
	}
	return handler.ErrNotFound
}

func (s *stubChannelStore) AddMember(_ context.Context, channelID, userID int64, role int16) error {
	s.members = append(s.members, repo.ChannelMember{
		ChannelID: channelID,
		UserID:    userID,
		Role:      role,
	})
	return nil
}

func (s *stubChannelStore) RemoveMember(_ context.Context, channelID, userID int64) error {
	var kept []repo.ChannelMember
	for _, m := range s.members {
		if !(m.ChannelID == channelID && m.UserID == userID) {
			kept = append(kept, m)
		}
	}
	s.members = kept
	return nil
}

func (s *stubChannelStore) GetMember(_ context.Context, channelID, userID int64) (*repo.ChannelMember, error) {
	for i := range s.members {
		if s.members[i].ChannelID == channelID && s.members[i].UserID == userID {
			m := s.members[i]
			return &m, nil
		}
	}
	return nil, handler.ErrNotFound
}

func (s *stubChannelStore) ListMembers(_ context.Context, channelID int64) ([]repo.ChannelMember, error) {
	var result []repo.ChannelMember
	for _, m := range s.members {
		if m.ChannelID == channelID {
			result = append(result, m)
		}
	}
	return result, nil
}

func (s *stubChannelStore) ListByUserWithPreview(_ context.Context, userID int64) ([]repo.ChannelWithPreview, error) {
	var result []repo.ChannelWithPreview
	for _, m := range s.members {
		if m.UserID == userID {
			for _, ch := range s.channels {
				if ch.ID == m.ChannelID {
					result = append(result, repo.ChannelWithPreview{Channel: ch})
					break
				}
			}
		}
	}
	return result, nil
}

func (s *stubChannelStore) FindDM(_ context.Context, userA, userB int64) (*repo.Channel, error) {
	// Find channels where both userA and userB are members and type == DM
	memberOf := func(uid, chID int64) bool {
		for _, m := range s.members {
			if m.UserID == uid && m.ChannelID == chID {
				return true
			}
		}
		return false
	}
	for _, ch := range s.channels {
		if ch.Type == repo.ChannelTypeDM && memberOf(userA, ch.ID) && memberOf(userB, ch.ID) {
			c := ch
			return &c, nil
		}
	}
	return nil, repo.ErrNotFound
}

// ---------- stub ChannelUserStore ----------

type stubChannelUserStore struct{}

func (s *stubChannelUserStore) GetByID(_ context.Context, id int64) (*repo.User, error) {
	return &repo.User{ID: id, Username: "user", DisplayName: "User"}, nil
}

// ---------- test helpers ----------

func newChannelHandler(t *testing.T) (*handler.ChannelHandler, *stubChannelStore) {
	t.Helper()
	cs := newStubChannelStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewChannelHandler(cs, &stubChannelUserStore{}, log)
	return h, cs
}

// ---------- tests ----------

func TestChannelHandler_CreateGroup_Success(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels", 1, map[string]any{
		"name":       "test-group",
		"member_ids": []int64{2, 3},
	})
	rr := httptest.NewRecorder()
	h.CreateGroup(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_CreateGroup_NoName(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels", 1, map[string]any{"name": ""})
	rr := httptest.NewRecorder()
	h.CreateGroup(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestChannelHandler_CreateOrGetDM_CreatesNew(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 2})
	rr := httptest.NewRecorder()
	h.CreateOrGetDM(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_CreateOrGetDM_ReturnsExisting(t *testing.T) {
	h, cs := newChannelHandler(t)
	// Create the DM first
	req1 := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 2})
	rr1 := httptest.NewRecorder()
	h.CreateOrGetDM(rr1, req1)
	if rr1.Code != http.StatusCreated {
		t.Fatalf("first DM: expected 201, got %d", rr1.Code)
	}
	_ = cs

	// Call again — should return existing (200)
	req2 := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 2})
	rr2 := httptest.NewRecorder()
	h.CreateOrGetDM(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("second DM: expected 200, got %d: %s", rr2.Code, rr2.Body.String())
	}
}

func TestChannelHandler_CreateOrGetDM_SelfError(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("POST", "/api/channels/dm", 1, map[string]any{"peer_id": 1})
	rr := httptest.NewRecorder()
	h.CreateOrGetDM(rr, req)
	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", rr.Code)
	}
}

func TestChannelHandler_ListChannels_Empty(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := requestWithClaims("GET", "/api/channels", 1, nil)
	rr := httptest.NewRecorder()
	h.ListChannels(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestChannelHandler_ListMembers_NonMemberForbidden(t *testing.T) {
	h, cs := newChannelHandler(t)
	// Create a group as user 1
	ch := &repo.Channel{Type: repo.ChannelTypeGroup, Name: "g"}
	cs.Create(context.Background(), ch)
	cs.AddMember(context.Background(), ch.ID, 1, repo.MemberRoleOwner)

	// User 2 (not a member) tries to list members
	req := requestWithClaims("GET", "/api/channels/1/members", 2, nil)
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	h.ListMembers(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_LeaveChannel_OwnerBlocked(t *testing.T) {
	h, cs := newChannelHandler(t)
	ch := &repo.Channel{Type: repo.ChannelTypeGroup, Name: "g"}
	cs.Create(context.Background(), ch)
	cs.AddMember(context.Background(), ch.ID, 1, repo.MemberRoleOwner)

	req := requestWithClaims("POST", "/api/channels/1/leave", 1, nil)
	req.SetPathValue("id", "1")
	rr := httptest.NewRecorder()
	h.LeaveChannel(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for owner leave, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestChannelHandler_NoAuth(t *testing.T) {
	h, _ := newChannelHandler(t)
	req := httptest.NewRequest("GET", "/api/channels", nil)
	rr := httptest.NewRecorder()
	h.ListChannels(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}
