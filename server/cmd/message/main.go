package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"im-server/internal/config"
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

// pulsarPushEvent is a mirror of gateway.PulsarPushEvent.
// Duplicated here to avoid circular imports.
type pulsarPushEvent struct {
	PushID    string  `json:"push_id"`
	TargetUID int64   `json:"target_uid"`
	ChannelID int64   `json:"channel_id"`
	Seq       int64   `json:"seq"`
	ServerID  int64   `json:"server_msg_id"`
	SenderID  int64   `json:"sender_id"`
	Content   string  `json:"content,omitempty"`
	MsgType   int16   `json:"msg_type"`
	VisibleTo []int64 `json:"visible_to,omitempty"`
	CreatedAt string  `json:"created_at"` // RFC3339
}

// ---------- service ----------

type messageService struct {
	messages     repo.MessageRepo
	channels     repo.ChannelRepo
	routing      *repo.Routing
	producer     *imPulsar.Producer // publishes deliver ACK to msg.deliver.{gateway_id}
	pushProducer *imPulsar.Producer // publishes push events to msg.push.{gateway_id}
	log          *slog.Logger
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

// pushToMembers publishes a PulsarPushEvent to msg.push.{gatewayID} for every
// member of msg.ChannelID that currently has an active connection.
func (svc *messageService) pushToMembers(ctx context.Context, msg *repo.Message) {
	if svc.pushProducer == nil || svc.channels == nil || svc.routing == nil {
		return
	}

	members, err := svc.channels.ListMembers(ctx, msg.ChannelID)
	if err != nil {
		svc.log.Warn("pushToMembers: list members failed", "error", err)
		return
	}

	for _, member := range members {
		gatewayIDs, err := svc.routing.GatewayIDsForUser(ctx, member.UserID)
		if err != nil || len(gatewayIDs) == 0 {
			continue // user offline, skip
		}

		// Determine visibility for this member.
		isVisible := msg.VisibleTo == nil || containsID(msg.VisibleTo, member.UserID)
		pushMsgType := int16(1) // normal
		content := msg.Content
		visibleTo := []int64(msg.VisibleTo)
		if !isVisible {
			pushMsgType = 2 // phantom: strip content so offline user sees a placeholder
			content = ""
			visibleTo = nil
		}

		pushID := fmt.Sprintf("%d-%d-%d", msg.ChannelID, msg.Seq, member.UserID)
		event := pulsarPushEvent{
			PushID:    pushID,
			TargetUID: member.UserID,
			ChannelID: msg.ChannelID,
			Seq:       msg.Seq,
			ServerID:  msg.ID,
			SenderID:  msg.SenderID,
			Content:   content,
			MsgType:   pushMsgType,
			VisibleTo: visibleTo,
			CreatedAt: msg.CreatedAt.Format(time.RFC3339),
		}

		for _, gwID := range gatewayIDs {
			topic := "msg.push." + gwID
			key := fmt.Sprintf("%d", member.UserID)
			if err := svc.pushProducer.Send(ctx, key, event); err != nil {
				svc.log.Warn("push event send failed", "topic", topic, "uid", member.UserID, "error", err)
			}
		}
	}
}

func containsID(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
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

	// Producer for push fan-out events (best-effort)
	pushProducer, err := pulsarClient.NewProducer("msg.push.fanout")
	if err != nil {
		log.Warn("could not create push producer (non-fatal)", "error", err)
		pushProducer = nil
	} else {
		defer pushProducer.Close()
	}

	// Redis client for routing lookups
	redisCtx, redisCancel := context.WithTimeout(context.Background(), 5*time.Second)
	rdb, redisErr := repo.OpenRedis(redisCtx, cfg.Redis.Addr, cfg.Redis.Password, cfg.Redis.DB)
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
		messages:     messageRepo,
		channels:     channelRepo,
		routing:      routing,
		producer:     deliverProducer,
		pushProducer: pushProducer,
		log:          log,
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
