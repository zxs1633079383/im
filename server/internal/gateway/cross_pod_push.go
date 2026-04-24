package gateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strconv"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// routingBatchLookup is the narrow routing surface CrossPodBroadcast needs.
// Real implementation: *Routing.LookupBatch. Tests inject stubs.
type routingBatchLookup interface {
	LookupBatch(ctx context.Context, userIDs []int64) (map[int64][]string, error)
}

// producerGetter returns a sender for a given topic. Implemented by
// *ProducerCache; tests inject stubs.
type producerGetter interface {
	GetOrCreate(ctx context.Context, topic string) (crossPodSender, error)
}

// crossPodSender is the minimal producer surface the broadcast loop needs.
type crossPodSender interface {
	Send(ctx context.Context, key string, payload any) error
}

// CrossPodBroadcast delivers (msgType, payload) to the union of userIDs across
// every gateway pod that currently hosts at least one of them.
//
// Flow:
//  1. Local fan-out — push to whatever conns this hub already holds. Each
//     successfully pushed user drops out of the "remote" set for free.
//  2. Batch lookup — one Redis pipelined HGETALL returns the gatewayID list
//     for every remaining user (N round-trips → 1).
//  3. Aggregate by gatewayID — bucketing N users onto M pods means one
//     Pulsar producer.Send per destination pod, carrying every affected user.
//     Payload is json.Marshal'd once and shared across buckets.
//  4. Envelope — the wire message is PulsarPushEnvelope{TargetUIDs, MsgType,
//     Payload}. The receiving pod iterates TargetUIDs, finds the local conns
//     for each, and pushes them the payload as a WS frame of type MsgType.
//
// partitionKey sets the Pulsar message key (partition routing). For channel
// broadcasts pass strconv.FormatInt(channelID, 10) so same-channel messages
// keep order. For single-user targeted events (read_sync, friend_event) pass
// strconv.FormatInt(userID, 10).
//
// This method supersedes the original CrossPodPush (kept as a thin wrapper
// below for callers that still operate on one user at a time).
func (h *Hub) CrossPodBroadcast(
	ctx context.Context,
	userIDs []int64,
	partitionKey string,
	msgType WSMessageType,
	payload any,
	routing *Routing,
	cache *ProducerCache,
	gatewayID string,
	env string,
	log *slog.Logger,
) {
	if len(userIDs) == 0 {
		return
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		log.Warn("cross pod broadcast: marshal payload", "error", err, "type", string(msgType))
		return
	}

	remote := h.pushLocalAndCollectRemote(userIDs, msgType, rawPayload)
	if len(remote) == 0 {
		return
	}
	h.crossPodBroadcastImpl(ctx, remote, partitionKey, msgType, rawPayload,
		routingBatchAdapter{routing}, producerCacheAdapter{cache},
		gatewayID, env, log)
}

// pushLocalAndCollectRemote fans rawPayload out to users that are connected
// on this pod and returns the remainder (those needing cross-pod routing).
// Shared by CrossPodBroadcast so the Pulsar path never sees local-only users.
func (h *Hub) pushLocalAndCollectRemote(
	userIDs []int64, msgType WSMessageType, rawPayload json.RawMessage,
) []int64 {
	remote := make([]int64, 0, len(userIDs))
	for _, uid := range userIDs {
		if sent := h.PushRawToUser(uid, msgType, rawPayload); sent == 0 {
			remote = append(remote, uid)
		}
	}
	return remote
}

// CrossPodPush is the single-user convenience wrapper around CrossPodBroadcast.
// Kept for existing call sites that operate on one user at a time (read_sync,
// friend_event, channel_event). New channel-wide fan-outs should prefer
// CrossPodBroadcast directly to collapse N Pulsar sends into M (one per pod).
func (h *Hub) CrossPodPush(
	ctx context.Context,
	userID int64,
	msgType WSMessageType,
	payload any,
	routing *Routing,
	cache *ProducerCache,
	gatewayID string,
	env string,
	log *slog.Logger,
) {
	h.CrossPodBroadcast(ctx, []int64{userID},
		strconv.FormatInt(userID, 10), msgType, payload,
		routing, cache, gatewayID, env, log)
}

// crossPodBroadcastImpl is the routing + cache half of CrossPodBroadcast.
// Extracted so it can run with stubbed routing / producer cache in tests.
func (h *Hub) crossPodBroadcastImpl(
	ctx context.Context,
	userIDs []int64,
	partitionKey string,
	msgType WSMessageType,
	rawPayload json.RawMessage,
	routing routingBatchLookup,
	cache producerGetter,
	gatewayID string,
	env string,
	log *slog.Logger,
) {
	gwMap, err := routing.LookupBatch(ctx, userIDs)
	if err != nil {
		log.Warn("cross pod broadcast: lookup batch failed", "error", err)
		return
	}
	buckets := bucketByGateway(gwMap, gatewayID)
	if len(buckets) == 0 {
		log.Info("offline fanout deferred",
			"uids", len(userIDs),
			"type", string(msgType),
		)
		return
	}
	h.sendBuckets(ctx, buckets, partitionKey, msgType, rawPayload, cache, env, log)
}

// bucketByGateway flips the per-user {uid → []gwID} map into the per-pod
// {gwID → []uid} shape the Pulsar send loop needs. Users already handled by
// the local hub (gwID == self) and duplicates are filtered out so each
// destination pod receives each user at most once.
func bucketByGateway(gwMap map[int64][]string, selfGatewayID string) map[string][]int64 {
	buckets := make(map[string][]int64, len(gwMap))
	for uid, gwIDs := range gwMap {
		seen := make(map[string]struct{}, len(gwIDs))
		for _, gw := range gwIDs {
			if gw == "" || gw == selfGatewayID {
				continue
			}
			if _, dup := seen[gw]; dup {
				continue
			}
			seen[gw] = struct{}{}
			buckets[gw] = append(buckets[gw], uid)
		}
	}
	return buckets
}

// sendBuckets emits one Pulsar message per destination pod, carrying every
// user in that bucket as a PulsarPushEnvelope.TargetUIDs list.
func (h *Hub) sendBuckets(
	ctx context.Context,
	buckets map[string][]int64,
	partitionKey string,
	msgType WSMessageType,
	rawPayload json.RawMessage,
	cache producerGetter,
	env string,
	log *slog.Logger,
) {
	for gwID, uids := range buckets {
		topic := PushTopicFor(gwID, env)
		producer, err := cache.GetOrCreate(ctx, topic)
		if err != nil {
			log.Warn("cross pod broadcast: producer open failed",
				"gw", gwID, "topic", topic, "error", err)
			h.failures.RecordFailure(ctx, gwID, uids)
			continue
		}
		envelope := PulsarPushEnvelope{
			TargetUIDs: uids,
			MsgType:    msgType,
			Payload:    rawPayload,
		}
		h.sendOne(ctx, producer, topic, gwID, partitionKey, msgType, envelope, uids, log)
	}
}

// sendOne publishes a single envelope and records per-send tracing + metrics.
// On failure it also bumps the per-gateway failure counter so the
// markOfflineThreshold eviction path can kick in when a target pod is really
// gone. On success it resets the counter so an intermittent blip does not
// accumulate forever.
func (h *Hub) sendOne(
	ctx context.Context,
	producer crossPodSender,
	topic, gwID, partitionKey string,
	msgType WSMessageType,
	envelope PulsarPushEnvelope,
	uids []int64,
	log *slog.Logger,
) {
	sendCtx, span := tracer.Start(ctx, "CrossPodBroadcast.Send",
		trace.WithAttributes(
			attribute.String("target.gateway", gwID),
			attribute.String("push.topic", topic),
			attribute.String("push.type", string(msgType)),
			attribute.Int("target.user_count", len(uids)),
		))
	start := time.Now()
	err := producer.Send(sendCtx, partitionKey, envelope)
	recordPushMetrics(sendCtx, time.Since(start), string(msgType), err)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		span.RecordError(err)
	}
	span.End()
	if err != nil {
		log.Warn("cross pod broadcast: send failed",
			"gw", gwID, "topic", topic, "count", len(uids), "error", err)
		h.failures.RecordFailure(ctx, gwID, uids)
		return
	}
	h.failures.RecordSuccess(gwID)
}

// routingBatchAdapter adapts *Routing (which has LookupBatch) to the narrow
// routingBatchLookup interface. nil-safe so callers don't have to special-case.
type routingBatchAdapter struct{ r *Routing }

func (a routingBatchAdapter) LookupBatch(ctx context.Context, userIDs []int64) (map[int64][]string, error) {
	if a.r == nil {
		return nil, nil
	}
	return a.r.LookupBatch(ctx, userIDs)
}

// producerCacheAdapter adapts *ProducerCache to the producerGetter interface,
// narrowing *imPulsar.Producer to crossPodSender (only Send is used).
type producerCacheAdapter struct{ c *ProducerCache }

func (a producerCacheAdapter) GetOrCreate(ctx context.Context, topic string) (crossPodSender, error) {
	if a.c == nil {
		return nil, errNilProducerCache
	}
	return a.c.GetOrCreate(ctx, topic)
}

// errNilProducerCache is returned when a broadcast is invoked without a
// cache. Indicates a wiring bug in cmd/gateway/main.go — tests rely on it
// to verify the nil-cache path cleanly.
var errNilProducerCache = &nilCacheErr{}

type nilCacheErr struct{}

func (*nilCacheErr) Error() string { return "cross pod push: producer cache is nil" }

// recordPushMetrics bumps im.push.pulsar.send (status + type tagged) and
// records im.pulsar.producer.send.duration. Safe even if the meter is noop.
func recordPushMetrics(ctx context.Context, dur time.Duration, msgType string, err error) {
	m := metrics()
	status := "ok"
	if err != nil {
		status = "error"
	}
	if m.PushSend != nil {
		m.PushSend.Add(ctx, 1, metric.WithAttributes(
			attribute.String("status", status),
			attribute.String("type", msgType),
		))
	}
	if m.PulsarSendDur != nil {
		m.PulsarSendDur.Record(ctx, float64(dur.Milliseconds()))
	}
}
