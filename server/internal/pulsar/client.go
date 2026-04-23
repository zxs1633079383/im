package pulsar

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	pulsarclient "github.com/apache/pulsar-client-go/pulsar"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// pulsarTracer is the OpenTelemetry tracer for Pulsar producer/consumer spans.
var pulsarTracer = otel.Tracer("im-pulsar")

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
	topic string
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
	return &Producer{inner: p, topic: topic, log: c.log}, nil
}

// Send JSON-encodes payload and publishes it to the topic.
// key is used as the partition routing key (e.g. channel_id as string ensures
// per-channel ordering).
//
// Send opens an OTel producer span and injects the resulting trace context
// into msg.Properties via the global TextMapPropagator. The matching consumer
// extracts the context from the same Properties map to continue the trace.
func (p *Producer) Send(ctx context.Context, key string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	ctx, span := pulsarTracer.Start(ctx, "pulsar.produce."+p.topic,
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.String("messaging.system", "pulsar"),
			attribute.String("messaging.destination.name", p.topic),
			attribute.String("messaging.operation", "publish"),
		))
	defer span.End()

	// Inject AFTER starting the span so the producer span context (not the
	// caller's parent) is what consumers extract.
	props := map[string]string{}
	otel.GetTextMapPropagator().Inject(ctx, propagation.MapCarrier(props))

	msg := &pulsarclient.ProducerMessage{
		Payload:    data,
		Properties: props,
	}
	if key != "" {
		msg.Key = key
	}
	if _, err = p.inner.Send(ctx, msg); err != nil {
		span.RecordError(err)
		return err
	}
	return nil
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
	topic   string
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
	return &Consumer{inner: consumer, topic: topic, handler: handler, log: c.log}, nil
}

// Consume starts a blocking consume loop. It stops when ctx is cancelled.
// Each message is dispatched to the handler; ACK on success, NACK on error.
//
// For each delivered message the loop extracts the OTel trace context from
// msg.Properties() (injected by the producer) and opens a SpanKindConsumer
// span named pulsar.consume.<topic>. The handler receives a context derived
// from this span so its downstream work joins the same trace.
func (cs *Consumer) Consume(ctx context.Context) error {
	for {
		msg, err := cs.inner.Receive(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("receive: %w", err)
		}

		msgCtx := otel.GetTextMapPropagator().Extract(ctx,
			propagation.MapCarrier(msg.Properties()))
		msgCtx, span := pulsarTracer.Start(msgCtx, "pulsar.consume."+cs.topic,
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				attribute.String("messaging.system", "pulsar"),
				attribute.String("messaging.source.name", cs.topic),
				attribute.String("messaging.operation", "process"),
			))

		if err := cs.handler(msgCtx, msg.Payload()); err != nil {
			span.RecordError(err)
			span.End()
			cs.log.Warn("message handler error, nacking", "error", err)
			cs.inner.Nack(msg)
			continue
		}
		span.End()
		cs.inner.Ack(msg)
	}
}

// Close releases the consumer.
func (cs *Consumer) Close() {
	cs.inner.Close()
}
