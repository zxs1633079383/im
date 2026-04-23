package gateway

import (
	"context"
	"fmt"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	imPulsar "im-server/internal/pulsar"
)

// producerCacheSize is the maximum number of live producers kept in the LRU.
// Evicted entries are closed via the onEvict callback.
const producerCacheSize = 256

// ProducerCache is an LRU of topic -> *imPulsar.Producer. It lets the gateway
// create a dedicated producer per remote pod (msg.push.{gwID}) on demand
// without reopening a connection on every fan-out.
//
// The cache is safe for concurrent use. Evicted producers are Close()d.
type ProducerCache struct {
	client *imPulsar.Client
	cache  *lru.Cache[string, *imPulsar.Producer]

	// createMu serializes creation of new producers for the same topic so we
	// don't open two producers for the same topic under concurrent misses.
	createMu sync.Mutex
}

// NewProducerCache creates a ProducerCache backed by an LRU of size producerCacheSize.
func NewProducerCache(client *imPulsar.Client) *ProducerCache {
	pc := &ProducerCache{client: client}
	cache, _ := lru.NewWithEvict[string, *imPulsar.Producer](producerCacheSize,
		func(_ string, p *imPulsar.Producer) {
			if p != nil {
				p.Close()
			}
		})
	pc.cache = cache
	return pc
}

// GetOrCreate returns a producer for topic, creating one on cache miss.
// The returned producer is owned by the cache and must NOT be closed by the caller.
func (pc *ProducerCache) GetOrCreate(_ context.Context, topic string) (*imPulsar.Producer, error) {
	if p, ok := pc.cache.Get(topic); ok {
		return p, nil
	}
	// Miss: serialize creation to avoid duplicate producers for the same topic.
	pc.createMu.Lock()
	defer pc.createMu.Unlock()
	if p, ok := pc.cache.Get(topic); ok {
		return p, nil
	}
	p, err := pc.client.NewProducer(topic)
	if err != nil {
		return nil, fmt.Errorf("producer cache: %w", err)
	}
	pc.cache.Add(topic, p)
	return p, nil
}

// Close flushes the LRU and closes every producer it holds.
func (pc *ProducerCache) Close() {
	pc.cache.Purge()
}
