package pulsar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	pulsarclient "github.com/apache/pulsar-client-go/pulsar"
)

// ---------- Client ----------

// Client owns the Pulsar connection. Create one per process and close on shutdown.
type Client struct {
	inner pulsarclient.Client
	log   *slog.Logger
}

// New creates a connected Pulsar client.
// url example: "pulsar://localhost:6650"
func New(url string, log *slog.Logger) (*Client, error) {
	c, err := pulsarclient.NewClient(pulsarclient.ClientOptions{URL: url})
	if err != nil {
		return nil, fmt.Errorf("pulsar new client: %w", err)
	}
	return &Client{inner: c, log: log}, nil
}

// Close releases the underlying connection.
func (c *Client) Close() {
	c.inner.Close()
}

// ---------- Producer ----------

// Producer sends JSON-encoded messages to a single Pulsar topic.
type Producer struct {
	inner pulsarclient.Producer
	log   *slog.Logger
}

// NewProducer creates a producer bound to the given topic.
// partitionKey, if non-empty, is applied to every message (ensures ordering per key).
func (c *Client) NewProducer(topic string) (*Producer, error) {
	p, err := c.inner.CreateProducer(pulsarclient.ProducerOptions{
		Topic: topic,
	})
	if err != nil {
		return nil, fmt.Errorf("create producer for %s: %w", topic, err)
	}
	return &Producer{inner: p, log: c.log}, nil
}

// Send JSON-encodes payload and publishes it to the topic.
// key is used as the partition routing key (e.g. channel_id as string ensures
// per-channel ordering).
func (p *Producer) Send(ctx context.Context, key string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	msg := &pulsarclient.ProducerMessage{
		Payload: data,
	}
	if key != "" {
		msg.Key = key
	}
	_, err = p.inner.Send(ctx, msg)
	return err
}

// Close releases the producer.
func (p *Producer) Close() {
	p.inner.Close()
}

// ---------- Consumer ----------

// HandlerFunc is the callback invoked for each incoming Pulsar message.
// If it returns nil, the message is ACKed. If it returns an error, the message
// is NACKed and will be redelivered.
type HandlerFunc func(ctx context.Context, data []byte) error

// Consumer reads messages from a Pulsar topic and dispatches them to a handler.
type Consumer struct {
	inner   pulsarclient.Consumer
	handler HandlerFunc
	log     *slog.Logger
}

// NewConsumer creates a consumer subscribed to the given topic.
// subscriptionName should be stable across restarts for at-least-once delivery.
func (c *Client) NewConsumer(topic, subscriptionName string, handler HandlerFunc) (*Consumer, error) {
	consumer, err := c.inner.Subscribe(pulsarclient.ConsumerOptions{
		Topic:            topic,
		SubscriptionName: subscriptionName,
		Type:             pulsarclient.Shared,
	})
	if err != nil {
		return nil, fmt.Errorf("subscribe to %s: %w", topic, err)
	}
	return &Consumer{inner: consumer, handler: handler, log: c.log}, nil
}

// Consume starts a blocking consume loop. It stops when ctx is cancelled.
// Each message is dispatched to the handler; ACK on success, NACk on error.
func (cs *Consumer) Consume(ctx context.Context) error {
	for {
		msg, err := cs.inner.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("receive: %w", err)
		}
		if err := cs.handler(ctx, msg.Payload()); err != nil {
			cs.log.Warn("message handler error, nacking", "error", err)
			cs.inner.Nack(msg)
			continue
		}
		cs.inner.Ack(msg)
	}
}

// Close releases the consumer.
func (cs *Consumer) Close() {
	cs.inner.Close()
}
