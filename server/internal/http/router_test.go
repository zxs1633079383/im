package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNew_HealthEndpoint(t *testing.T) {
	r := New(Config{ServiceName: "test"})

	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "ok", w.Body.String())
}

func TestNew_ReadyzEndpoint(t *testing.T) {
	r := New(Config{ServiceName: "test"})

	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
}

func TestNew_LegacyMuxFallthrough(t *testing.T) {
	mux := stdhttp.NewServeMux()
	mux.HandleFunc("/legacy", func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		_, _ = w.Write([]byte("legacy"))
	})
	r := New(Config{ServiceName: "test", Legacy: mux})

	req := httptest.NewRequest("GET", "/legacy", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	require.Equal(t, 200, w.Code)
	require.Equal(t, "legacy", w.Body.String())
}
