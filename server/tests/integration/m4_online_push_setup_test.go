//go:build integration

package integration

// 在线推送测试集合 — 辅助 wiring 文件。
//
// `m4_harness_test.go::buildEngine` 是 Batch-D 的 WS 基座，但有 2 个生产
// 路由未在 harness 中接通：
//   - DELETE /api/channels/:id           (RegisterChannelCloseRoute)
//   - PATCH  /api/channels/:id/members/:user_id/nickname (RegisterMemberNicknameRoute)
//
// 本测试集合（doc-05 完整链路 + PRD §5 15-step smoke）需要这些路由触发
// 对应 WSMessageType 帧落到 wsClient inbox。`m4_harness_test.go` 在
// worktree 黑名单中（基座 / 跨任务共享），所以走辅助文件 +
// 调用方在 newM4Env 后显式 wireOnlinePushExtras 一次接通。
//
// Wire path 完全镜像 `cmd/gateway/main.go::buildRouter`：
//   - close + nickname 路由复用 m4_harness_test.go 里的 localBroadcaster
//   - schedule_created / schedule_canceled 的 WS 帧暂无法接通（ScheduledService
//     pusher 是私有字段且 harness 已实例化）→ 走 hub.PushToUser 直推路径
//     (与 m4_ws_reaction_heartbeat_test.go::TestM4WSReadSync_HappyPath 同
//     风格)。HTTP-触发的端到端覆盖会随 harness 升级再加，详见 §B doc。

import (
	"log/slog"
	"testing"

	"im-server/internal/gateway"
	imhttp "im-server/internal/http"
	"im-server/internal/middleware"
	"im-server/internal/repo"
	"im-server/internal/service"
)

// wireOnlinePushExtras 把生产 `cmd/gateway/main.go` 已挂、但 harness
// 漏挂的 2 个 endpoint 接到 env.engine。
//
// 调用约束：必须在 newM4Env 后、wsDial 前调用一次。Gin engine 的
// route trie 在装配阶段是单线程安全的；httptest.Server 启动（即 wsDial）
// 后再加路由会触发 race。本辅助返回新建的 ChannelService 实例方便测试
// 直接验证 side-effect（例如 channel_member_updated{nickname} 推帧已落）。
func (e *m4env) wireOnlinePushExtras(t *testing.T) *service.ChannelService {
	t.Helper()

	log := slog.Default()

	authed := e.engine.Group("/api")
	authed.Use(middleware.MattermostCookieResolve(e.rdb, log))
	authed.Use(middleware.CookieRequired())

	// 1. ChannelService 重建一份 —— 与 m4_harness_test.go::buildEngine 内
	//    持有的实例独立，但共享底层 repo / hub / messages。
	//    为什么不复用：harness 里的 channelSvc 没被 export。Gin 允许
	//    一个 path 由不同 channelSvc 处理 N 次的 handler 注册（route trie
	//    会按"先到先得"匹配）；本辅助挂的 path 在 m4_harness_test.go 中
	//    完全没有 → 不会与既有 route 冲突。
	channelSvc := service.NewChannelService(e.channels, e.messages)
	channelEventRepo := repo.NewChannelEventRepo(e.db)
	channelSvc.AttachChannelEventRepo(channelEventRepo)

	// channel_member_updated 广播 hook —— 与生产装配一致。
	// LeaveChannel / RemoveMember / SetMemberNickname / TransferOwner 触发的
	// 帧由 service 内部 fanMemberUpdate 调用 broadcaster.BroadcastMemberEvent。
	broadcaster := &localBroadcaster{hub: e.hub, channels: e.channels}
	channelSvc.AttachMemberBroadcaster(broadcaster)

	// 2. DELETE /api/channels/:id — owner 解散群聊 + channel_closed WS 广播。
	imhttp.RegisterChannelCloseRoute(authed, channelSvc, broadcaster, log)
	// 3. PATCH /api/channels/:id/members/:user_id/nickname — 改群昵称
	//    + channel_member_updated{change_type=nickname}。
	imhttp.RegisterMemberNicknameRoute(authed, channelSvc, broadcaster, log)

	return channelSvc
}

// pushScheduleEventDirect 是 schedule_created / schedule_canceled 的本地
// 推送 helper —— harness 未注入 ScheduledEventPusher (pusher 字段私有，且
// ScheduledService 实例在 buildEngine 内不可达)，所以测试侧通过 hub 直推
// 等价的 gateway.ChannelSchedulePayload 验证 wire shape。与
// m4_ws_reaction_heartbeat_test.go::TestM4WSReadSync_HappyPath 同风格 —— 仅
// 跳过"HTTP 端点 → service → pusher" 内部那一段，wire 端的解码 + 多设备
// fan-out 仍完整测到。
func (e *m4env) pushScheduleEventDirect(
	t *testing.T,
	senderID string,
	eventType gateway.WSMessageType,
	payload gateway.ChannelSchedulePayload,
) {
	t.Helper()
	e.hub.PushToUser(senderID, eventType, payload)
}
