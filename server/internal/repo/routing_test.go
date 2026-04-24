package repo

import (
	"strings"
	"testing"
)

// TestConnKeyPrefix locks the Redis key layout. The prefix is a public
// contract: changing it invalidates every live routing entry in pre/prod
// and must be done through a coordinated rollout. If this test breaks you
// probably did not mean to change the prefix — update the schema migration
// plan first.
func TestConnKeyPrefix(t *testing.T) {
	got := connKey(42)
	const want = "im-new:routing:user:42"
	if got != want {
		t.Errorf("connKey(42) = %q, want %q", got, want)
	}
	if !strings.HasPrefix(got, routingKeyPrefix+":") {
		t.Errorf("connKey(42) = %q, missing prefix %q", got, routingKeyPrefix)
	}
}

func TestConnKeyPerUserUnique(t *testing.T) {
	a := connKey(1)
	b := connKey(2)
	if a == b {
		t.Errorf("connKey must encode userID: got %q == %q", a, b)
	}
}
