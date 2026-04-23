package testutil

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gavv/httpexpect/v2"
)

// NewExpect builds a fully wired httpexpect.Expect against an in-process
// httptest server hosting h. Cleanup (server shutdown) is registered.
//
// Phase 6+ Gin handler tests use this to assert HTTP behavior end-to-end
// without spinning up a real listener or external client.
func NewExpect(t *testing.T, h http.Handler) *httpexpect.Expect {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return httpexpect.Default(t, srv.URL)
}
