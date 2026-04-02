package gateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

const (
	heartbeatInterval = 15 * time.Second
	readDeadline      = 45 * time.Second
)

// runHeartbeat sends periodic pong frames to conn with the current channel seq
// diff (server_seq - client known_seq). It exits when the connection's send
// buffer is full (slow consumer) or the context is cancelled.
//
// The read deadline (45s) set on the WebSocket connection acts as the
// server-side liveness timeout: if no data arrives from the client within 45s,
// ReadMessage returns an error and the caller's readPump shuts down the conn.
func runHeartbeat(ctx context.Context, conn *Conn, channelSt ChannelSeqStore, log *slog.Logger) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		serverSeqs, err := channelSt.GetMemberChannelSeqs(fetchCtx, conn.UserID)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("heartbeat: get channel seqs failed",
				"error", err, "user_id", conn.UserID)
			continue
		}

		// Compute diff: channels where server_seq > client's known_seq.
		diff := make(map[string]int64, len(serverSeqs))
		for chID, serverSeq := range serverSeqs {
			if serverSeq > conn.KnownSeqFor(chID) {
				diff[fmt.Sprintf("%d", chID)] = serverSeq
			}
		}

		payload := PongPayload{
			ServerTime:  time.Now().UnixMilli(),
			ChannelSeqs: diff,
		}
		if !conn.Push(TypePong, payload) {
			log.Warn("heartbeat: send buffer full, closing conn",
				"user_id", conn.UserID, "device_id", conn.DeviceID)
			conn.ws.Close() //nolint:errcheck
			return
		}
	}
}
