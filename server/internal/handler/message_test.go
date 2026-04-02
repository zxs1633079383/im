package handler_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"im-server/internal/handler"
	"im-server/internal/model"
)

// ---------- stub MsgStore ----------

type stubMsgStore struct {
	messages []model.Message
	nextSeq  int64
}

func newStubMsgStore() *stubMsgStore { return &stubMsgStore{nextSeq: 1} }

func (s *stubMsgStore) Send(_ context.Context, msg *model.Message) error {
	// Idempotent: return existing if client_msg_id matches
	if msg.ClientMsgID != "" {
		for _, m := range s.messages {
			if m.ChannelID == msg.ChannelID && m.ClientMsgID == msg.ClientMsgID {
				msg.ID = m.ID
				msg.Seq = m.Seq
				return nil
			}
		}
	}
	msg.ID = s.nextSeq
	msg.Seq = s.nextSeq
	msg.CreatedAt = time.Now()
	s.nextSeq++
	s.messages = append(s.messages, *msg)
	return nil
}

func (s *stubMsgStore) FetchForUser(_ context.Context, channelID, userID int64, afterSeq int64, limit int) ([]model.Message, error) {
	var result []model.Message
	for _, m := range s.messages {
		if m.ChannelID == channelID && m.Seq > afterSeq {
			if m.IsVisibleTo(userID) {
				result = append(result, m)
			} else {
				result = append(result, model.Message{ChannelID: m.ChannelID, Seq: m.Seq, MsgType: model.MsgTypePhantom})
			}
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *stubMsgStore) FetchBefore(_ context.Context, channelID, userID int64, beforeSeq int64, limit int) ([]model.Message, error) {
	var result []model.Message
	for i := len(s.messages) - 1; i >= 0; i-- {
		m := s.messages[i]
		if m.ChannelID == channelID && m.Seq < beforeSeq {
			if m.IsVisibleTo(userID) {
				result = append(result, m)
			} else {
				result = append(result, model.Message{ChannelID: m.ChannelID, Seq: m.Seq, MsgType: model.MsgTypePhantom})
			}
			if len(result) >= limit {
				break
			}
		}
	}
	return result, nil
}

func (s *stubMsgStore) FetchAround(_ context.Context, channelID, userID int64, aroundSeq int64, limit int) ([]model.Message, error) {
	return s.FetchForUser(context.Background(), channelID, userID, 0, limit)
}

// ---------- stub MsgChannelStore ----------

type stubMsgChannelStore struct {
	members  map[int64]map[int64]*model.ChannelMember // channelID -> userID -> member
	channels map[int64]*model.Channel
}

func newStubMsgChannelStore() *stubMsgChannelStore {
	return &stubMsgChannelStore{
		members:  make(map[int64]map[int64]*model.ChannelMember),
		channels: make(map[int64]*model.Channel),
	}
}

func (s *stubMsgChannelStore) addMember(channelID, userID int64) {
	if s.members[channelID] == nil {
		s.members[channelID] = make(map[int64]*model.ChannelMember)
	}
	s.members[channelID][userID] = &model.ChannelMember{ChannelID: channelID, UserID: userID}
}

func (s *stubMsgChannelStore) addChannel(ch *model.Channel) {
	s.channels[ch.ID] = ch
}

func (s *stubMsgChannelStore) GetMember(_ context.Context, channelID, userID int64) (*model.ChannelMember, error) {
	if cm := s.members[channelID][userID]; cm != nil {
		return cm, nil
	}
	return nil, handler.ErrNotFound
}

func (s *stubMsgChannelStore) MarkRead(_ context.Context, channelID, userID, seq int64) error {
	if cm := s.members[channelID][userID]; cm != nil {
		cm.LastReadSeq = seq
	}
	return nil
}

func (s *stubMsgChannelStore) GetByID(_ context.Context, id int64) (*model.Channel, error) {
	if ch, ok := s.channels[id]; ok {
		return ch, nil
	}
	return nil, handler.ErrNotFound
}

// ---------- helpers ----------

func newMessageHandler(t *testing.T) (*handler.MessageHandler, *stubMsgStore, *stubMsgChannelStore) {
	t.Helper()
	ms := newStubMsgStore()
	cs := newStubMsgChannelStore()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	h := handler.NewMessageHandler(ms, cs, log)
	return h, ms, cs
}

// ---------- tests ----------

func TestMessageHandler_SendMessage_Success(t *testing.T) {
	h, _, cs := newMessageHandler(t)
	cs.addMember(1, 42)

	req := requestWithClaims("POST", "/api/channels/1/messages", 42, map[string]any{
		"content":       "hello",
		"client_msg_id": "uuid-001",
	})
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.SendMessage(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMessageHandler_SendMessage_NotMember(t *testing.T) {
	h, _, _ := newMessageHandler(t) // no members added

	req := requestWithClaims("POST", "/api/channels/1/messages", 42, map[string]any{
		"content": "hello",
	})
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.SendMessage(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestMessageHandler_SendMessage_EmptyContent(t *testing.T) {
	h, _, cs := newMessageHandler(t)
	cs.addMember(1, 42)

	req := requestWithClaims("POST", "/api/channels/1/messages", 42, map[string]any{
		"content": "",
	})
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.SendMessage(w, req)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422, got %d", w.Code)
	}
}

func TestMessageHandler_SendMessage_Idempotent(t *testing.T) {
	h, ms, cs := newMessageHandler(t)
	cs.addMember(1, 42)

	body := map[string]any{"content": "hello", "client_msg_id": "uuid-dup"}

	req1 := requestWithClaims("POST", "/api/channels/1/messages", 42, body)
	req1.SetPathValue("id", "1")
	w1 := httptest.NewRecorder()
	h.SendMessage(w1, req1)

	req2 := requestWithClaims("POST", "/api/channels/1/messages", 42, body)
	req2.SetPathValue("id", "1")
	w2 := httptest.NewRecorder()
	h.SendMessage(w2, req2)

	if len(ms.messages) != 1 {
		t.Errorf("expected 1 message (idempotent), got %d", len(ms.messages))
	}
	if w1.Code != http.StatusCreated || w2.Code != http.StatusCreated {
		t.Errorf("both sends should be 201")
	}
}

func TestMessageHandler_FetchMessages_AfterSeq(t *testing.T) {
	h, ms, cs := newMessageHandler(t)
	cs.addMember(1, 42)
	// Pre-populate store with two messages
	ms.Send(context.Background(), &model.Message{ChannelID: 1, SenderID: 42, Content: "a", MsgType: model.MsgTypeText})
	ms.Send(context.Background(), &model.Message{ChannelID: 1, SenderID: 42, Content: "b", MsgType: model.MsgTypeText})

	req := requestWithClaims("GET", "/api/channels/1/messages?after_seq=0", 42, nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.FetchMessages(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestMessageHandler_FetchMessages_NotMember(t *testing.T) {
	h, _, _ := newMessageHandler(t)

	req := requestWithClaims("GET", "/api/channels/1/messages?after_seq=0", 99, nil)
	req.SetPathValue("id", "1")
	w := httptest.NewRecorder()

	h.FetchMessages(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", w.Code)
	}
}

func TestMessageHandler_MarkRead_Success(t *testing.T) {
	h, _, cs := newMessageHandler(t)
	cs.addMember(10, 7)
	cs.addChannel(&model.Channel{ID: 10, Seq: 5})

	req := requestWithClaims("POST", "/api/channels/10/read", 7, nil)
	req.SetPathValue("id", "10")
	w := httptest.NewRecorder()

	h.MarkRead(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if cs.members[10][7].LastReadSeq != 5 {
		t.Errorf("LastReadSeq = %d, want 5", cs.members[10][7].LastReadSeq)
	}
}
