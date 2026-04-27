package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	imPulsar "im-server/internal/pulsar"
)

// ackRegistry is a lightweight in-process structure that lets the push consumer
// await client ACKs for a given push_id. The push consumer registers a channel,
// and the read pump resolves it when a push_ack frame arrives.
type ackRegistry struct {
	mu      sync.Mutex
	waiters map[string]chan struct{}
}

// resolve signals that the client has ACKed the given push_id.
// If no waiter is registered, this is a no-op.
func (a *ackRegistry) resolve(pushID string) {
	a.mu.Lock()
	ch, ok := a.waiters[pushID]
	if ok {
		delete(a.waiters, pushID)
	}
	a.mu.Unlock()
	if ok {
		close(ch)
	}
}

// await registers a waiter for pushID and returns a channel that is closed
// when the ACK arrives. The caller is responsible for removing the waiter
// if it times out before the ACK.
func (a *ackRegistry) await(pushID string) <-chan struct{} {
	ch := make(chan struct{})
	a.mu.Lock()
	a.waiters[pushID] = ch
	a.mu.Unlock()
	return ch
}

// cancel removes a pending waiter without signalling it (used on timeout cleanup).
func (a *ackRegistry) cancel(pushID string) {
	a.mu.Lock()
	delete(a.waiters, pushID)
	a.mu.Unlock()
}

// globalACKRegistry is the process-wide push ACK registry.
// The push consumer uses await/cancel; the read pump uses resolve.
var globalACKRegistry = &ackRegistry{
	waiters: make(map[string]chan struct{}),
}

// ---------- PushConsumer ----------

const (
	ackTimeout    = 3 * time.Second
	retryInterval = 5 * time.Second
	maxRetries    = 1
)

// PushConsumer subscribes to msg.push.{gatewayID} and delivers messages
// to connected WebSocket clients via the Hub.
type PushConsumer struct {
	hub       *Hub
	gatewayID string
	env       string
	log       *slog.Logger
}

// NewPushConsumer creates a PushConsumer. env must match the sender side's
// gateway env (prod/pre/local) so the subscribe topic agrees with
// PushTopicFor. A blank env (for legacy call sites) falls back to
// PushTopicFor's "local" bucket.
func NewPushConsumer(hub *Hub, gatewayID, env string, log *slog.Logger) *PushConsumer {
	return &PushConsumer{hub: hub, gatewayID: gatewayID, env: env, log: log}
}

// Topic returns the Pulsar topic this consumer should subscribe to. Uses
// PushTopicFor so sender + receiver share a single source of truth.
func (pc *PushConsumer) Topic() string {
	return PushTopicFor(pc.gatewayID, pc.env)
}

// SubscriptionName returns a stable subscription name for at-least-once delivery.
func (pc *PushConsumer) SubscriptionName() string {
	return "gateway-push-" + pc.gatewayID
}

// Start creates a Pulsar consumer and begins consuming in a background goroutine.
// Returns a stop function; call it to shut down the consumer.
func (pc *PushConsumer) Start(ctx context.Context, pulsarClient *imPulsar.Client) error {
	consumer, err := pulsarClient.NewConsumer(pc.Topic(), pc.SubscriptionName(), pc.Handle)
	if err != nil {
		return fmt.Errorf("push consumer subscribe: %w", err)
	}
	go func() {
		defer consumer.Close()
		if err := consumer.Consume(ctx); err != nil {
			pc.log.Error("push consumer exited with error", "error", err)
		}
	}()
	return nil
}

// Handle is the Pulsar HandlerFunc. It processes one PulsarPushEnvelope by
// iterating the envelope's TargetUIDs, looking up local conns for each,
// and pushing the shared raw payload as a WS frame of type env.MsgType.
//
// push_msg style ACK tracking: when the inner payload carries a push_id
// (e.g. PushMsgPayload), deliverWithRetry waits for the client ACK on the
// first recipient with live conns. Other payload types (read_sync,
// friend_event, system messages) skip ACK tracking and rely on pong-diff
// recovery for robustness.
func (pc *PushConsumer) Handle(ctx context.Context, data []byte) error {
	var env PulsarPushEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("unmarshal push envelope: %w", err)
	}
	if len(env.TargetUIDs) == 0 {
		pc.log.Debug("push envelope has no target uids")
		return nil
	}

	pushID := extractPushID(env.Payload)
	delivered := 0
	for _, uid := range env.TargetUIDs {
		if pc.deliverOne(ctx, uid, env.MsgType, env.Payload, pushID) {
			delivered++
		}
	}
	if delivered == 0 {
		pc.log.Debug("push: no target conns on this pod",
			"count", len(env.TargetUIDs), "type", string(env.MsgType))
	}
	// Always ACK the Pulsar message. Client-side ACK failures are covered by
	// pong-diff fallback; Pulsar redelivery would amplify storms here.
	return nil
}

// deliverOne pushes rawPayload to every local conn of uid and, for push_msg
// frames, waits on the client ACK via the shared ackRegistry. Returns true
// when at least one conn received the frame.
func (pc *PushConsumer) deliverOne(
	ctx context.Context,
	uid string,
	msgType WSMessageType,
	rawPayload json.RawMessage,
	pushID string,
) bool {
	conns := pc.hub.ConnsForUser(uid)
	if len(conns) == 0 {
		return false
	}
	// ACK tracking is only meaningful for push_msg delivery (that's what the
	// client ACKs). Other types go fire-and-forget; pong-diff handles loss.
	if msgType == TypePushMsg && pushID != "" {
		return pc.deliverWithRetry(ctx, pushID, rawPayload, conns)
	}
	sent := 0
	for _, c := range conns {
		if c.PushRaw(msgType, rawPayload) {
			sent++
		}
	}
	return sent > 0
}

// deliverWithRetry pushes payload to conns and waits for an ACK up to
// ackTimeout. It performs at most maxRetries+1 total attempts. Returns true
// if any conn received the frame (ACK is a best-effort signal — the client
// may ACK late or not at all; pong-diff recovers either way).
func (pc *PushConsumer) deliverWithRetry(
	ctx context.Context,
	pushID string,
	rawPayload json.RawMessage,
	conns []*Conn,
) bool {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-time.After(retryInterval):
			case <-ctx.Done():
				return false
			}
		}
		ackCh := globalACKRegistry.await(pushID)
		sent := 0
		for _, c := range conns {
			if c.PushRaw(TypePushMsg, rawPayload) {
				sent++
			}
		}
		if sent == 0 {
			globalACKRegistry.cancel(pushID)
			pc.log.Warn("push: all send buffers full, skipping retry",
				"push_id", pushID, "attempt", attempt)
			return false
		}
		select {
		case <-ackCh:
			pc.log.Debug("push ack received", "push_id", pushID, "attempt", attempt)
			return true
		case <-time.After(ackTimeout):
			globalACKRegistry.cancel(pushID)
		case <-ctx.Done():
			globalACKRegistry.cancel(pushID)
			return false
		}
	}
	return false
}

// extractPushID peeks at a payload's push_id field without decoding the whole
// struct. Payloads without push_id (read_sync, friend_event, etc.) return "".
func extractPushID(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var probe struct {
		PushID string `json:"push_id"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return probe.PushID
}
