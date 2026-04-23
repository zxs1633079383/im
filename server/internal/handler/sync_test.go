package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"im-server/internal/auth"
	"im-server/internal/repo"
)

// ---------- test helpers ----------

const syncTestSecret = "test-secret-32-bytes-long-enough!"

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}

func ctxWithClaims(ctx context.Context, userID int64) context.Context {
	c := &auth.Claims{}
	c.UserID = userID
	return context.WithValue(ctx, ClaimsKey, c)
}

// ---------- stub stores ----------

type stubSyncChannelStore struct {
	seqs   map[int64]int64
	member *repo.ChannelMember
	getErr error
}

func (s *stubSyncChannelStore) GetMemberChannelSeqs(_ context.Context, _ int64) (map[int64]int64, error) {
	return s.seqs, s.getErr
}

func (s *stubSyncChannelStore) GetMember(_ context.Context, _, _ int64) (*repo.ChannelMember, error) {
	if s.member != nil {
		return s.member, nil
	}
	return &repo.ChannelMember{}, nil
}

func (s *stubSyncChannelStore) GetByID(_ context.Context, _ int64) (*repo.Channel, error) {
	return &repo.Channel{}, nil
}

type stubSyncMsgStore struct {
	messages []repo.Message
}

func (s *stubSyncMsgStore) FetchForUser(_ context.Context, _, _ int64, afterSeq int64, limit int) ([]repo.Message, error) {
	var result []repo.Message
	for _, m := range s.messages {
		if m.Seq > afterSeq && len(result) < limit {
			result = append(result, m)
		}
	}
	return result, nil
}

// ---------- helpers ----------

func makeSyncRequest(t *testing.T, userID int64, jwtSecret string, reqBody SyncRequest) *http.Request {
	t.Helper()
	token, err := auth.GenerateToken(jwtSecret, userID, "testuser")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	body, _ := json.Marshal(reqBody)
	r := httptest.NewRequest(http.MethodPost, "/api/sync", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	return r
}

// ---------- tests ----------

func TestSyncHandler_NoChanges(t *testing.T) {
	chStore := &stubSyncChannelStore{
		seqs: map[int64]int64{1: 100, 2: 200},
	}
	msgStore := &stubSyncMsgStore{}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	// Client is up-to-date on both channels.
	req := makeSyncRequest(t, 42, syncTestSecret, SyncRequest{
		Channels: []SyncChannelEntry{{ID: 1, Seq: 100}, {ID: 2, Seq: 200}},
	})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 0 {
		t.Errorf("expected 0 channel results, got %d", len(resp.Channels))
	}
}

func TestSyncHandler_SmallGap(t *testing.T) {
	chStore := &stubSyncChannelStore{
		seqs:   map[int64]int64{1: 105},
		member: &repo.ChannelMember{LastReadSeq: 100, PhantomCount: 0, PhantomAtRead: 0},
	}
	// Pretend messages 101–105 exist.
	var msgs []repo.Message
	for i := int64(101); i <= 105; i++ {
		msgs = append(msgs, repo.Message{ChannelID: 1, Seq: i, Content: "msg"})
	}
	msgStore := &stubSyncMsgStore{messages: msgs}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	req := makeSyncRequest(t, 42, syncTestSecret, SyncRequest{
		Channels: []SyncChannelEntry{{ID: 1, Seq: 100}},
	})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 channel result, got %d", len(resp.Channels))
	}
	ch := resp.Channels[0]
	if ch.HasMore {
		t.Error("small gap should not set has_more")
	}
	if len(ch.Messages) != 5 {
		t.Errorf("expected 5 messages, got %d", len(ch.Messages))
	}
	if ch.Unread != 5 {
		t.Errorf("expected unread=5, got %d", ch.Unread)
	}
}

func TestSyncHandler_LargeGap(t *testing.T) {
	chStore := &stubSyncChannelStore{
		seqs: map[int64]int64{1: 300},
	}
	// Build 300 messages (seq 1–300).
	var msgs []repo.Message
	for i := int64(1); i <= 300; i++ {
		msgs = append(msgs, repo.Message{ChannelID: 1, Seq: i, Content: "msg"})
	}
	msgStore := &stubSyncMsgStore{messages: msgs}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	// Client is at seq 0 (never synced this channel). Gap = 300 > syncGapThreshold=100.
	req := makeSyncRequest(t, 42, syncTestSecret, SyncRequest{
		Channels: []SyncChannelEntry{{ID: 1, Seq: 0}},
	})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 result, got %d", len(resp.Channels))
	}
	ch := resp.Channels[0]
	if !ch.HasMore {
		t.Error("large gap should set has_more=true")
	}
	if len(ch.Messages) > syncMsgLimit {
		t.Errorf("should return at most %d messages, got %d", syncMsgLimit, len(ch.Messages))
	}
}

func TestSyncHandler_NewChannel(t *testing.T) {
	// Server knows about channel 99 (joined while client was offline).
	chStore := &stubSyncChannelStore{
		seqs: map[int64]int64{99: 10},
	}
	var msgs []repo.Message
	for i := int64(1); i <= 10; i++ {
		msgs = append(msgs, repo.Message{ChannelID: 99, Seq: i, Content: "welcome"})
	}
	msgStore := &stubSyncMsgStore{messages: msgs}
	h := NewSyncHandler(chStore, msgStore, testLogger())

	// Client sends empty channel list (doesn't know about channel 99).
	req := makeSyncRequest(t, 42, syncTestSecret, SyncRequest{Channels: []SyncChannelEntry{}})
	req = req.WithContext(ctxWithClaims(req.Context(), 42))
	w := httptest.NewRecorder()
	h.Sync(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp SyncResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if len(resp.Channels) != 1 {
		t.Fatalf("expected 1 new-channel result, got %d", len(resp.Channels))
	}
	ch := resp.Channels[0]
	if ch.ID != 99 {
		t.Errorf("expected channel 99, got %d", ch.ID)
	}
	if len(ch.Messages) == 0 {
		t.Error("expected messages for new channel")
	}
}

func TestSyncHandler_Unauthorized(t *testing.T) {
	h := NewSyncHandler(&stubSyncChannelStore{}, &stubSyncMsgStore{}, testLogger())
	r := httptest.NewRequest(http.MethodPost, "/api/sync", bytes.NewReader([]byte("{}")))
	// No claims in context — simulates missing JWT.
	w := httptest.NewRecorder()
	h.Sync(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}
