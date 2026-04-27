package repo

import (
	"strings"
	"testing"
)

// TestConnKeyPrefix locks the Redis key layout. M4: connKey now interpolates
// the mm UserID (24-char hex string) so the prefix is a public contract that
// must be coordinated with cses Redis routing.
func TestConnKeyPrefix(t *testing.T) {
	got := connKey("676cc4ccfbbc501161d5cd65")
	const want = "im-new:routing:user:676cc4ccfbbc501161d5cd65"
	if got != want {
		t.Errorf("connKey(...) = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, routingKeyPrefix+":") {
		t.Errorf("connKey(...) = %q, missing prefix %q", got, routingKeyPrefix)
	}
}

func TestConnKeyPerUserUnique(t *testing.T) {
	a := connKey("111111111111111111111111")
	b := connKey("222222222222222222222222")
	if a == b {
		t.Errorf("connKey must encode userID: got %q == %q", a, b)
	}
}
