package gateway

import (
	"context"
	"log/slog"
	"strconv"
)

// routingLookup is the narrow interface CrossPodPush needs from *Routing.
// Declaring it here lets tests inject a stub without standing up Redis.
type routingLookup interface {
	Lookup(ctx context.Context, userID int64) ([]string, error)
}

// producerGetter is the narrow interface CrossPodPush needs from *ProducerCache.
// It must return an object with a Send(ctx, key, payload) method.
type producerGetter interface {
	GetOrCreate(ctx context.Context, topic string) (crossPodSender, error)
}

// crossPodSender is the minimal producer surface used by CrossPodPush.
type crossPodSender interface {
	Send(ctx context.Context, key string, payload any) error
}

// CrossPodPush delivers (msgType, payload) to all online pods of userID.
//
// Flow:
//  1. Local hit — if the user has connections on this hub, push locally and
//     return. Routing lookup is skipped on the hot path.
//  2. Remote fan-out — iterate gateway IDs from routing.Lookup, skipping the
//     current pod. For each remote gwID, resolve or open a cached producer
//     keyed by PushTopicFor(gwID, env) and publish the payload with the user
//     ID as the partition key (so per-user ordering is preserved).
//  3. All offline — log and return. M2 will back this branch with an offline
//     message store; for now the HTTP fallback (incremental sync) recovers.
//
// The routing, cache, gatewayID, env and logger are passed in (not held on
// Hub) to keep Hub's zero-value tests cheap and to avoid a wiring cycle.
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
	// 1. Local hit short-circuit.
	if sent := h.PushToUser(userID, msgType, payload); sent > 0 {
		return
	}

	// 2. Remote fan-out — use the minimal-interface form so the core logic is
	// testable without real Redis / Pulsar.
	h.crossPodPushImpl(ctx, userID, msgType, payload,
		routingAdapter{routing}, producerCacheAdapter{cache},
		gatewayID, env, log)
}

// crossPodPushImpl is the routing/cache half of CrossPodPush, extracted so it
// can be exercised with stubs in unit tests. Callers have already confirmed
// there is no local hit.
func (h *Hub) crossPodPushImpl(
	ctx context.Context,
	userID int64,
	msgType WSMessageType,
	payload any,
	routing routingLookup,
	cache producerGetter,
	gatewayID string,
	env string,
	log *slog.Logger,
) {
	gwIDs, err := routing.Lookup(ctx, userID)
	if err != nil {
		log.Warn("cross pod push: routing lookup failed", "uid", userID, "error", err)
		return
	}
	if len(gwIDs) == 0 {
		log.Info("offline fanout deferred",
			"uid", userID,
			"type", string(msgType),
		)
		return
	}

	key := strconv.FormatInt(userID, 10)
	delivered := 0
	for _, gwID := range gwIDs {
		if gwID == gatewayID {
			continue // self — already handled by local hit check
		}
		topic := PushTopicFor(gwID, env)
		producer, err := cache.GetOrCreate(ctx, topic)
		if err != nil {
			log.Warn("cross pod push: producer open failed",
				"gw", gwID, "topic", topic, "error", err)
			continue
		}
		if err := producer.Send(ctx, key, payload); err != nil {
			log.Warn("cross pod push: send failed",
				"gw", gwID, "topic", topic, "error", err)
			continue
		}
		delivered++
	}

	if delivered == 0 {
		log.Info("offline fanout deferred",
			"uid", userID,
			"type", string(msgType),
		)
	}
}

// routingAdapter adapts *Routing (which has Lookup) to the routingLookup
// interface. *Routing already has a Lookup method so the adapter is a
// straight pass-through — nil-safe so callers don't have to special-case it.
type routingAdapter struct{ r *Routing }

func (a routingAdapter) Lookup(ctx context.Context, userID int64) ([]string, error) {
	if a.r == nil {
		return nil, nil
	}
	return a.r.Lookup(ctx, userID)
}

// producerCacheAdapter adapts *ProducerCache to producerGetter. The adapter
// narrows the *imPulsar.Producer concrete type to the crossPodSender interface
// so the core loop depends only on Send.
type producerCacheAdapter struct{ c *ProducerCache }

func (a producerCacheAdapter) GetOrCreate(ctx context.Context, topic string) (crossPodSender, error) {
	if a.c == nil {
		return nil, errNilProducerCache
	}
	return a.c.GetOrCreate(ctx, topic)
}

// errNilProducerCache is returned when CrossPodPush is invoked without a cache.
// In production this would indicate a wiring bug in main.go; in tests it lets
// us verify the nil-cache path cleanly.
var errNilProducerCache = &nilCacheErr{}

type nilCacheErr struct{}

func (*nilCacheErr) Error() string { return "cross pod push: producer cache is nil" }
