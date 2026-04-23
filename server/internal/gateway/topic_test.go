package gateway

import (
	"testing"
)

func TestPushTopicFor_PerEnv(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("HOSTNAME", "")

	tests := []struct {
		name      string
		gatewayID string
		env       string
		want      string
	}{
		{
			name:      "prod namespace",
			gatewayID: "gw-abc",
			env:       "prod",
			want:      "persistent://im/push/msg.push.gw-abc",
		},
		{
			name:      "pre namespace",
			gatewayID: "gw-abc",
			env:       "pre",
			want:      "persistent://im/push-pre/msg.push.gw-abc",
		},
		{
			name:      "local namespace uses USER suffix",
			gatewayID: "gw-abc",
			env:       "local",
			want:      "persistent://im/push-local/msg.push.gw-abc.alice",
		},
		{
			name:      "unknown env falls into local bucket",
			gatewayID: "gw-xyz",
			env:       "staging",
			want:      "persistent://im/push-local/msg.push.gw-xyz.alice",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PushTopicFor(tc.gatewayID, tc.env)
			if got != tc.want {
				t.Fatalf("PushTopicFor(%q, %q) = %q; want %q", tc.gatewayID, tc.env, got, tc.want)
			}
		})
	}
}

func TestPushTopicFor_LocalFallbackToAnon(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("HOSTNAME", "")

	got := PushTopicFor("gw-1", "local")
	want := "persistent://im/push-local/msg.push.gw-1.anon"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}

func TestPushTopicFor_LocalFallbackToHostname(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("HOSTNAME", "devbox")

	got := PushTopicFor("gw-1", "local")
	want := "persistent://im/push-local/msg.push.gw-1.devbox"
	if got != want {
		t.Fatalf("got %q; want %q", got, want)
	}
}
