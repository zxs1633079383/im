//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/require"

	"im-server/internal/gateway"
	"im-server/internal/middleware"
)

// ---- WS test fixture --------------------------------------------------------
//
// Each WS test starts an httptest.Server bound to env.engine, dials
// ws://.../ws with the cookieId header (the same auth path production uses),
// and runs a goroutine that pumps every server frame onto an *frameInbox*
// channel so test bodies can synchronously expect the next frame of a given
// type with a deadline.
//
// Cleanup is layered:
//   - t.Cleanup closes the conn first (which triggers writePump exit)
//   - then closes the inbox so any blocked t.expect_frame returns immediately
//   - then shuts down the httptest.Server
//
// Per harness/C002 § cross-pod push, the local conn delivery path the WS
// fixtures exercise is the "same pod" branch — Pulsar fan-out is not
// exercised here (Batch-D scope is single-pod happy paths).

// wsClient is an authenticated WS connection plus the receive-side plumbing.
type wsClient struct {
	t      *testing.T
	conn   *websocket.Conn
	server *httptest.Server
	inbox  chan gateway.WSFrame
	pumpWg sync.WaitGroup
	closed chan struct{}
}

// wsDial starts a httptest.Server bound to env.engine, dials /ws with the
// given cookieId header, and starts the receive pump. The fixture owns the
// returned wsClient — call wc.Close (registered via t.Cleanup automatically)
// to tear everything down at end of test.
func wsDial(t *testing.T, env *m4env, cookieID string) *wsClient {
	t.Helper()

	server := httptest.NewServer(env.engine)
	wsURL := strings.Replace(server.URL, "http://", "ws://", 1) + "/ws"

	header := http.Header{}
	header.Set(middleware.MMCookieHeader, cookieID)

	dialer := websocket.DefaultDialer
	dialCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := dialer.DialContext(dialCtx, wsURL, header)
	require.NoError(t, err, "ws dial %s", wsURL)

	wc := &wsClient{
		t:      t,
		conn:   conn,
		server: server,
		inbox:  make(chan gateway.WSFrame, 32),
		closed: make(chan struct{}),
	}

	wc.pumpWg.Add(1)
	go wc.readPump()

	t.Cleanup(wc.Close)
	return wc
}

// wireFrame mirrors the anonymous struct gateway.Conn marshals/decodes to
// (see conn.go::Push — Payload is json.RawMessage so the inner object stays
// untouched). gateway.WSFrame uses []byte for Payload which json-marshals to
// base64; not the wire shape, so we cannot use it for client-side codec.
type wireFrame struct {
	Type    gateway.WSMessageType `json:"type"`
	Payload json.RawMessage       `json:"payload,omitempty"`
}

// readPump drains incoming frames into wc.inbox until the conn errors out
// (typical: writePump closed by Close, or read deadline exceeded). Exits
// cleanly so race detector stays happy.
func (wc *wsClient) readPump() {
	defer wc.pumpWg.Done()
	for {
		_, raw, err := wc.conn.ReadMessage()
		if err != nil {
			return
		}
		var w wireFrame
		if err := json.Unmarshal(raw, &w); err != nil {
			wc.t.Logf("wsClient: drop unparseable frame: %v", err)
			continue
		}
		frame := gateway.WSFrame{Type: w.Type, Payload: []byte(w.Payload)}
		select {
		case wc.inbox <- frame:
		case <-wc.closed:
			return
		}
	}
}

// Close tears down the connection, the read pump, and the httptest.Server.
// Idempotent — t.Cleanup may invoke it after the test body already did.
func (wc *wsClient) Close() {
	select {
	case <-wc.closed:
		return
	default:
	}
	close(wc.closed)
	_ = wc.conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	_ = wc.conn.Close()
	wc.pumpWg.Wait()
	wc.server.Close()
}

// writeFrame builds the {type, payload: <raw json>} wire envelope and sends
// it. payload is marshalled into json.RawMessage so the inner object stays
// untouched on the wire (matches gateway.Conn.Push' format).
func (wc *wsClient) writeFrame(msgType gateway.WSMessageType, payload any) {
	wc.t.Helper()
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		require.NoError(wc.t, err, "marshal ws payload")
		raw = b
	}
	require.NoError(wc.t, wc.conn.WriteJSON(wireFrame{Type: msgType, Payload: raw}),
		"write ws frame %s", msgType)
}

// expectFrame waits up to timeout for the next frame whose Type equals want
// and returns it. Any unmatched frames consumed in the meantime are
// discarded (with a Logf trail) — Batch-D happy paths are deterministic so
// the desired frame should be at the head of the inbox.
func (wc *wsClient) expectFrame(want gateway.WSMessageType, timeout time.Duration) gateway.WSFrame {
	wc.t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case f, ok := <-wc.inbox:
			if !ok {
				wc.t.Fatalf("ws inbox closed before %s arrived", want)
			}
			if f.Type == want {
				return f
			}
			wc.t.Logf("wsClient: skip frame type=%s while waiting for %s", f.Type, want)
		case <-deadline.C:
			wc.t.Fatalf("timeout waiting for ws frame type=%s", want)
		}
	}
	// unreachable — the for-select above either returns or t.Fatalf'd.
}

// decodePayload unmarshals frame.Payload into out. Convenience for tests that
// want to assert on payload fields beyond the envelope type.
func decodePayload(t *testing.T, frame gateway.WSFrame, out any) {
	t.Helper()
	require.NoError(t, json.Unmarshal(frame.Payload, out), "decode ws payload")
}

// ---- Reference test ---------------------------------------------------------

// TestM4WS_SendACK_HappyPath is the canonical smoke that proves wsDial +
// expectFrame work end-to-end against the harness-wired ws_handler. Client
// sends a `send` frame on a DM it owns; server persists the message and
// returns send_ack with the assigned server_msg_id and seq.
//
// Reference test uses send/send_ack rather than ping/pong because pong is
// pushed by the 15s heartbeat tick (would force every WS test to wait 15s+).
//
// Seed range 900-999 is reserved for Batch-D.
func TestM4WS_SendACK_HappyPath(t *testing.T) {
	env := newM4Env(t)
	cookieSender, _ := env.seedUser(900)
	_, recvID := env.seedUser(901)
	channelID := env.seedDM(cookieSender, recvID)

	wc := wsDial(t, env, cookieSender)
	wc.writeFrame(gateway.TypeSend, gateway.SendPayload{
		ClientMsgID: "ws-ref-001",
		ChannelID:   channelID,
		Content:     "hello via WS",
		MsgType:     1,
	})

	ack := wc.expectFrame(gateway.TypeSendACK, 5*time.Second)
	var p gateway.SendACKPayload
	decodePayload(t, ack, &p)
	require.Equal(t, "ws-ref-001", p.ClientMsgID, "send_ack must echo client_msg_id")
	require.NotEmpty(t, p.ServerMsgID, "send_ack must carry server_msg_id")
	require.Greater(t, p.Seq, int64(0), "send_ack must carry seq")
	require.Equal(t, channelID, p.ChannelID, "send_ack channel_id must match send")
	_ = fmt.Sprintf("%v", p) // race detector exercise
}
