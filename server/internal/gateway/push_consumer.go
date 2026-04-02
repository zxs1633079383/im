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
	log       *slog.Logger
}

// NewPushConsumer creates a PushConsumer.
func NewPushConsumer(hub *Hub, gatewayID string, log *slog.Logger) *PushConsumer {
	return &PushConsumer{hub: hub, gatewayID: gatewayID, log: log}
}

// Topic returns the Pulsar topic this consumer should subscribe to.
func (pc *PushConsumer) Topic() string {
	return "msg.push." + pc.gatewayID
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

// Handle is the Pulsar HandlerFunc. It processes one PulsarPushEvent: finds
// target user connections in the Hub, pushes the message (full or phantom based
// on visibility), and waits up to ackTimeout for a client ACK (1 retry).
func (pc *PushConsumer) Handle(ctx context.Context, data []byte) error {
	var event PulsarPushEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return fmt.Errorf("unmarshal push event: %w", err)
	}

	conns := pc.hub.ConnsForUser(event.TargetUID)
	if len(conns) == 0 {
		// User not connected to this gateway pod — routing should have prevented
		// this, but log and ACK to avoid redelivery.
		pc.log.Debug("push: no connections for user", "uid", event.TargetUID, "push_id", event.PushID)
		return nil
	}

	// Build payload. MsgType==2 means phantom: strip content and visible_to.
	payload := buildPushPayload(event)

	// Attempt delivery with 1 retry.
	acked := pc.deliverWithRetry(ctx, event.PushID, event.ChannelID, event.Seq, payload, conns)
	if !acked {
		pc.log.Debug("push ack not received — pull fallback will catch up",
			"push_id", event.PushID, "uid", event.TargetUID)
	}

	// Always ACK the Pulsar message to avoid redelivery storm.
	// Pull-based fallback (heartbeat pong diff) covers missed pushes.
	return nil
}

// deliverWithRetry pushes payload to conns and waits for an ACK up to ackTimeout.
// It performs at most maxRetries+1 total attempts. Returns true if ACKed.
func (pc *PushConsumer) deliverWithRetry(
	ctx context.Context,
	pushID string,
	channelID int64,
	seq int64,
	payload PushMsgPayload,
	conns []*Conn,
) bool {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// Wait retryInterval before re-pushing.
			select {
			case <-time.After(retryInterval):
			case <-ctx.Done():
				return false
			}
		}

		ackCh := globalACKRegistry.await(pushID)

		sent := 0
		for _, c := range conns {
			if c.Push(TypePushMsg, payload) {
				sent++
				c.UpdateKnownSeq(channelID, seq)
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
			pc.log.Debug("push ack timeout", "push_id", pushID, "attempt", attempt)
			// Continue to next attempt if retries remain.
		case <-ctx.Done():
			globalACKRegistry.cancel(pushID)
			return false
		}
	}
	return false
}

// buildPushPayload converts a PulsarPushEvent to the wire PushMsgPayload.
// For phantom messages (MsgType==2, VisibleTo set and target is not in list),
// content is already stripped by the message service before publishing.
func buildPushPayload(event PulsarPushEvent) PushMsgPayload {
	createdAt, _ := time.Parse(time.RFC3339, event.CreatedAt)
	return PushMsgPayload{
		PushID:    event.PushID,
		ChannelID: event.ChannelID,
		Seq:       event.Seq,
		ServerID:  event.ServerID,
		SenderID:  event.SenderID,
		Content:   event.Content,
		MsgType:   event.MsgType,
		VisibleTo: event.VisibleTo,
		CreatedAt: createdAt,
	}
}
