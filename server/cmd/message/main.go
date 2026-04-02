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
	"im-server/internal/model"
	imPulsar "im-server/internal/pulsar"
	"im-server/internal/store"
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

// ---------- service ----------

type messageService struct {
	store    *store.MessageStore
	producer *imPulsar.Producer // publishes to msg.deliver.{gateway_id}
	log      *slog.Logger
}

func (svc *messageService) handle(ctx context.Context, data []byte) error {
	var in incomingMessage
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("unmarshal incoming: %w", err)
	}

	msgType := model.MsgType(in.MsgType)
	if msgType == 0 {
		msgType = model.MsgTypeText
	}

	msg := &model.Message{
		ChannelID:   in.ChannelID,
		SenderID:    in.SenderID,
		ClientMsgID: in.ClientMsgID,
		MsgType:     msgType,
		Content:     in.Content,
		VisibleTo:   in.VisibleTo,
		ReplyTo:     in.ReplyTo,
	}

	if err := svc.store.Send(ctx, msg); err != nil {
		return fmt.Errorf("store.Send: %w", err)
	}

	svc.log.Info("message persisted",
		"channel_id", msg.ChannelID,
		"seq", msg.Seq,
		"client_msg_id", msg.ClientMsgID,
	)

	// Publish delivery event so Gateway can ACK the sender (Plan 6 will consume this).
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
			// Non-fatal: log and continue. The sender will get their ACK via
			// the HTTP response (Plan 5) or pong heartbeat (Plan 6).
			svc.log.Warn("publish delivery event failed", "topic", topic, "error", err)
		}
	}

	return nil
}

// ---------- run ----------

func run() int {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfgPath := os.Getenv("IM_CONFIG")
	if cfgPath == "" {
		cfgPath = "config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Error("load config", "error", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	pool, err := store.NewPGPool(ctx, cfg.PG.DSN, cfg.PG.MaxConns)
	cancel()
	if err != nil {
		log.Error("connect to postgres", "error", err)
		return 1
	}
	defer pool.Close()

	msgStore := store.NewMessageStore(pool)

	// Connect to Pulsar
	pulsarClient, err := imPulsar.New(cfg.Pulsar.URL, log)
	if err != nil {
		log.Error("connect to pulsar", "error", err)
		return 1
	}
	defer pulsarClient.Close()

	// Producer for delivery ACK events (best-effort, Plan 6 consumes)
	deliverProducer, err := pulsarClient.NewProducer("msg.deliver.ack")
	if err != nil {
		log.Warn("could not create delivery producer (non-fatal)", "error", err)
		deliverProducer = nil
	} else {
		defer deliverProducer.Close()
	}

	svc := &messageService{
		store:    msgStore,
		producer: deliverProducer,
		log:      log,
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
