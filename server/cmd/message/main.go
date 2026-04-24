package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"im-server/internal/config"
	"im-server/internal/gateway"
	"im-server/internal/observability"
	imPulsar "im-server/internal/pulsar"
	"im-server/internal/repo"

	"github.com/lib/pq"
)

func main() {
	fmt.Println("message service starting...")
	os.Exit(run())
}

// ---------- wire types ----------

type incomingMessage struct {
	GatewayID   string  `json:"gateway_id"`
	ChannelID   int64   `json:"channel_id"`
	SenderID    int64   `json:"sender_id"`
	ClientMsgID string  `json:"client_msg_id"`
	MsgType     int16   `json:"msg_type"`
	Content     string  `json:"content"`
	VisibleTo   []int64 `json:"visible_to,omitempty"`
	ReplyTo     *int64  `json:"reply_to,omitempty"`
}

type deliveryEvent struct {
	ClientMsgID string `json:"client_msg_id"`
	ServerMsgID int64  `json:"server_msg_id"`
	ChannelID   int64  `json:"channel_id"`
	Seq         int64  `json:"seq"`
	SenderID    int64  `json:"sender_id"`
}

// (legacy pulsarPushEvent has been deleted — cmd/message now publishes via
// gateway.PulsarPushEnvelope, bucketing recipients by gatewayID so each
// destination pod gets one Pulsar Send carrying every affected user, not one
// per user.)

// ---------- service ----------

type messageService struct {
	messages repo.MessageRepo
	channels repo.ChannelRepo
	routing  *repo.Routing
	// producer publishes the delivery ACK back to msg.deliver.{gateway_id}
	// so the originating gateway can mark the sender's send_ack. Still a
	// single-topic single-user envelope; unrelated to the push fan-out.
	producer *imPulsar.Producer
	// pushCache opens one producer per push topic (msg.push.{gwID}) on demand
	// and is the batched replacement for the legacy single-topic producer.
	pushCache *gateway.ProducerCache
	// env selects the Pulsar namespace when building push topics
	// (prod / pre / other → dev-suffixed).
	env string
	log *slog.Logger
}

func (svc *messageService) handle(ctx context.Context, data []byte) error {
	var in incomingMessage
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("unmarshal incoming: %w", err)
	}

	msgType := in.MsgType
	if msgType == 0 {
		msgType = repo.MsgTypeText
	}

	msg := &repo.Message{
		ChannelID:   in.ChannelID,
		SenderID:    in.SenderID,
		ClientMsgID: in.ClientMsgID,
		MsgType:     msgType,
		Content:     in.Content,
		VisibleTo:   pq.Int64Array(in.VisibleTo),
		ReplyTo:     in.ReplyTo,
	}

	if err := svc.messages.Send(ctx, msg); err != nil {
		return fmt.Errorf("messages.Send: %w", err)
	}

	svc.log.Info("message persisted",
		"channel_id", msg.ChannelID,
		"seq", msg.Seq,
		"client_msg_id", msg.ClientMsgID,
	)

	// Publish delivery ACK so gateway can ACK the sender.
	if in.GatewayID != "" && svc.producer != nil {
		event := deliveryEvent{
			ClientMsgID: msg.ClientMsgID,
			ServerMsgID: msg.ID,
			ChannelID:   msg.ChannelID,
			Seq:         msg.Seq,
			SenderID:    msg.SenderID,
		}
		topic := "msg.deliver." + in.GatewayID
		key := fmt.Sprintf("%d", msg.SenderID)
		if err := svc.producer.Send(ctx, key, event); err != nil {
			svc.log.Warn("publish delivery event failed", "topic", topic, "error", err)
		}
	}

	// Fan out push events to every channel member's gateway pod.
	svc.pushToMembers(ctx, msg)

	return nil
}

// pushToMembers publishes gateway.PulsarPushEnvelope messages for every online
// channel member, bucketed by destination gatewayID so each pod gets exactly
// one Pulsar Send per visibility bucket. Same batching contract as
// cmd/gateway/hubMessagePusher.BroadcastMessage, but driven from the
// standalone delivery worker.
func (svc *messageService) pushToMembers(ctx context.Context, msg *repo.Message) {
	if svc.pushCache == nil || svc.channels == nil || svc.routing == nil {
		return
	}
	members, err := svc.channels.ListMembers(ctx, msg.ChannelID)
	if err != nil {
		svc.log.Warn("pushToMembers: list members failed", "error", err)
		return
	}
	if len(members) == 0 {
		return
	}
	visible, phantom := bucketByVisibility(members, msg)
	if len(visible) > 0 {
		svc.broadcastBucket(ctx, msg, visible, false)
	}
	if len(phantom) > 0 {
		svc.broadcastBucket(ctx, msg, phantom, true)
	}
}

// broadcastBucket marshals a single payload variant (real or phantom) and
// spreads it across every destination pod hosting at least one of userIDs.
// routing.LookupBatch does one Redis round-trip for the whole set so we pay
// O(1) network per call regardless of bucket size.
func (svc *messageService) broadcastBucket(
	ctx context.Context, msg *repo.Message, userIDs []int64, phantomVariant bool,
) {
	payload := buildPushPayload(msg, phantomVariant)
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		svc.log.Warn("marshal push payload", "error", err)
		return
	}
	gwMap, err := svc.routing.LookupBatch(ctx, userIDs)
	if err != nil {
		svc.log.Warn("pushToMembers: lookup batch failed", "error", err)
		return
	}
	buckets := bucketByGateway(gwMap)
	msgType := gateway.TypePushMsg
	partitionKey := strconv.FormatInt(msg.ChannelID, 10)
	for gwID, uids := range buckets {
		topic := gateway.PushTopicFor(gwID, svc.env)
		producer, err := svc.pushCache.GetOrCreate(ctx, topic)
		if err != nil {
			svc.log.Warn("push producer open failed",
				"gw", gwID, "topic", topic, "error", err)
			continue
		}
		envelope := gateway.PulsarPushEnvelope{
			TargetUIDs: uids,
			MsgType:    msgType,
			Payload:    rawPayload,
		}
		if err := producer.Send(ctx, partitionKey, envelope); err != nil {
			svc.log.Warn("push envelope send failed",
				"gw", gwID, "topic", topic, "count", len(uids), "error", err)
		}
	}
}

// bucketByVisibility splits members into a visible bucket (sees real content)
// and a phantom bucket (sees placeholder); the sender always lands in visible
// so they see their own message.
func bucketByVisibility(members []repo.ChannelMember, msg *repo.Message) (visible, phantom []int64) {
	for _, m := range members {
		if msg.VisibleTo == nil || msg.IsVisibleTo(m.UserID) || m.UserID == msg.SenderID {
			visible = append(visible, m.UserID)
		} else {
			phantom = append(phantom, m.UserID)
		}
	}
	return visible, phantom
}

// bucketByGateway groups (uid → gwID list) into (gwID → uid list) skipping
// offline users (empty gwID list). De-duplicates per-uid multi-device entries
// so the same envelope does not carry duplicate TargetUIDs.
func bucketByGateway(gwMap map[int64][]string) map[string][]int64 {
	out := make(map[string][]int64, len(gwMap))
	for uid, gwIDs := range gwMap {
		seen := make(map[string]struct{}, len(gwIDs))
		for _, gw := range gwIDs {
			if gw == "" {
				continue
			}
			if _, dup := seen[gw]; dup {
				continue
			}
			seen[gw] = struct{}{}
			out[gw] = append(out[gw], uid)
		}
	}
	return out
}

// buildPushPayload returns the gateway.PushMsgPayload that represents msg
// for a given visibility branch. The wire shape is identical to the one
// produced by cmd/gateway's hubMessagePusher so both producers interoperate
// against the same PushConsumer.Handle on the receiving pod.
func buildPushPayload(msg *repo.Message, phantomVariant bool) gateway.PushMsgPayload {
	if phantomVariant {
		return gateway.PushMsgPayload{
			PushID:    fmt.Sprintf("msg-%d-%d", msg.ChannelID, msg.Seq),
			ChannelID: msg.ChannelID,
			Seq:       msg.Seq,
			MsgType:   repo.MsgTypePhantom,
			CreatedAt: msg.CreatedAt,
		}
	}
	return gateway.PushMsgPayload{
		PushID:    fmt.Sprintf("msg-%d-%d", msg.ChannelID, msg.Seq),
		ChannelID: msg.ChannelID,
		Seq:       msg.Seq,
		ServerID:  msg.ID,
		SenderID:  msg.SenderID,
		Content:   msg.Content,
		MsgType:   msg.MsgType,
		VisibleTo: []int64(msg.VisibleTo),
		CreatedAt: msg.CreatedAt,
	}
}

// ---------- run ----------

func run() int {
	baseHandler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	log := slog.New(observability.NewTraceHandler(baseHandler))
	slog.SetDefault(log)

	otelShutdown, err := observability.Init(context.Background(), observability.Config{
		ServiceName:    "im-message",
		ServiceVersion: "dev",
		Disabled:       os.Getenv("OTEL_DISABLED") == "true",
	})
	if err != nil {
		log.Error("otel init", "error", err)
		return 1
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = otelShutdown(shutCtx)
	}()

	cfgPath := os.Getenv("IM_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("load config", "error", err)
		return 1
	}

	// Open the GORM-backed Postgres connection. The repo package owns the
	// pool; we close the underlying *sql.DB on shutdown.
	gormDB, err := repo.Open(repo.Config{DSN: cfg.PG.DSN, MaxOpen: cfg.PG.MaxConns})
	if err != nil {
		log.Error("connect to postgres", "error", err)
		return 1
	}
	defer func() {
		if sqlDB, e := gormDB.DB(); e == nil {
			_ = sqlDB.Close()
		}
	}()

	channelRepo := repo.NewChannelRepo(gormDB)
	messageRepo := repo.NewMessageRepo(gormDB, channelRepo)

	// Connect to Pulsar
	pulsarClient, err := imPulsar.New(cfg.Pulsar.URL, log)
	if err != nil {
		log.Error("connect to pulsar", "error", err)
		return 1
	}
	defer pulsarClient.Close()

	// Producer for delivery ACK events (best-effort)
	deliverProducer, err := pulsarClient.NewProducer("msg.deliver.ack")
	if err != nil {
		log.Warn("could not create delivery producer (non-fatal)", "error", err)
		deliverProducer = nil
	} else {
		defer deliverProducer.Close()
	}

	// One producer per msg.push.{gwID} topic, opened on demand and cached.
	// Replaces the old single-topic producer that accidentally fanned every
	// push to msg.push.fanout — wrong topology for the per-pod receiver setup.
	pushCache := gateway.NewProducerCache(pulsarClient)
	defer pushCache.Close()

	env := os.Getenv("IM_ENV")
	if env == "" {
		env = "local"
	}

	// Redis client for routing lookups
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 5*time.Second)
	rdb, redisErr := repo.OpenRedis(redisCtx, repo.RedisOptions{
		Addrs:    cfg.Redis.ResolveAddrs(),
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
		Cluster:  cfg.Redis.Cluster,
	})
	redisCancel()
	if redisErr != nil {
		log.Warn("could not connect to redis — push fan-out disabled (non-fatal)", "error", redisErr)
	}

	var routing *repo.Routing
	if redisErr == nil {
		// gatewayID is empty string for routing lookup — GatewayIDsForUser does not use it.
		routing = repo.NewRouting(rdb, "")
	}

	svc := &messageService{
		messages:  messageRepo,
		channels:  channelRepo,
		routing:   routing,
		producer:  deliverProducer,
		pushCache: pushCache,
		env:       env,
		log:       log,
	}

	consumer, err := pulsarClient.NewConsumer("msg.incoming", "message-service", svc.handle)
	if err != nil {
		log.Error("create consumer", "error", err)
		return 1
	}
	defer consumer.Close()

	// Graceful shutdown
	runCtx, runCancel := context.WithCancel(context.Background())
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Info("shutting down...")
		runCancel()
	}()

	log.Info("consuming from msg.incoming")
	if err := consumer.Consume(runCtx); err != nil {
		log.Error("consumer error", "error", err)
		return 1
	}

	return 0
}
