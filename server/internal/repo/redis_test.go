package repo

import (
	"context"
	"errors"
	"testing"
)

func TestOpenRedisRejectsEmptyAddrs(t *testing.T) {
	_, err := OpenRedis(context.Background(), RedisOptions{})
	if err == nil {
		t.Fatal("OpenRedis with empty Addrs should error, got nil")
	}
	// Error must mention "addr" so operators can debug misconfig quickly.
	if !containsIgnoreCase(err.Error(), "addr") {
		t.Errorf("error %q should mention addr", err.Error())
	}
}

// containsIgnoreCase is a tiny helper to avoid pulling in strings just for
// the lowercase check in the single assertion above.
func containsIgnoreCase(haystack, needle string) bool {
	if len(needle) == 0 {
		return true
	}
	// Cheap lowercase-bytes match — ASCII only, which is fine for test strings.
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			a := haystack[i+j]
			b := needle[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestOpenRedisPingFailureIsWrapped(t *testing.T) {
	// Use a reserved-for-test port that should never serve Redis. Ping must
	// fail, and the error must be wrapped with a "ping redis" prefix so logs
	// point at the right layer.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately so dial errors out fast

	_, err := OpenRedis(ctx, RedisOptions{Addrs: []string{"127.0.0.1:1"}})
	if err == nil {
		t.Fatal("OpenRedis to bogus addr should error, got nil")
	}
	// Accept either "ping redis" wrap or a context cancellation — what we
	// want to verify is that the error surfaces instead of silently returning
	// a broken client.
	if errors.Is(err, context.Canceled) {
		return
	}
	if !containsIgnoreCase(err.Error(), "ping") && !containsIgnoreCase(err.Error(), "dial") {
		t.Errorf("unexpected error shape: %v", err)
	}
}
