package gateway

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTypeHello_ConstantWire 锁定 hello 帧的 type 字段值。
// cses-client `dispatch_v2.rs` 把 hello 列入 ROUTE_VIA_LEGACY_HANDLER 白名单，
// 改这个常量会导致客户端找不到 handler，sync_engine 永远不会启动。
func TestTypeHello_ConstantWire(t *testing.T) {
	require.Equal(t, WSMessageType("hello"), TypeHello,
		"TypeHello wire value 必须等于 \"hello\"（与 cses-client handlers/hello.rs 协议）")
}

// TestHelloPayload_JSONShape 锁定 hello payload 的 JSON 字段名。
// cses-client `handlers/hello.rs:20` 读 `msg.data.get("connectionId")`，
// 改字段名会导致 connectionId 无法注入 IM HTTP client default_headers。
func TestHelloPayload_JSONShape(t *testing.T) {
	p := HelloPayload{
		ConnectionID: "abc-123",
		ServerTime:   1700000000000,
	}
	b, err := json.Marshal(p)
	require.NoError(t, err)

	// 反序列化为 map 验证字段名严格匹配（不是 connection_id 也不是 ConnectionId）
	var got map[string]any
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, "abc-123", got["connectionId"],
		"connectionId 字段名是客户端 hello.rs 解析锚点，不能改")
	require.EqualValues(t, 1700000000000, got["server_time"],
		"server_time 字段名与 PongPayload 对齐")
}

// TestHelloPayload_RoundTrip 反序列化兼容性：保证 hello 帧 wire 形式
// 经 json.Marshal → bytes → json.Unmarshal 后字段值不变。
func TestHelloPayload_RoundTrip(t *testing.T) {
	original := HelloPayload{
		ConnectionID: "f47ac10b-58cc-4372-a567-0e02b2c3d479",
		ServerTime:   1758000000123,
	}
	b, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded HelloPayload
	require.NoError(t, json.Unmarshal(b, &decoded))
	require.Equal(t, original, decoded)
}
