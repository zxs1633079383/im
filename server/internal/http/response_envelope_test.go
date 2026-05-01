package http

import (
	"bytes"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestShouldSkipEnvelope locks down the path allow-list. Adding a new probe
// path requires updating both the function and this table.
func TestShouldSkipEnvelope(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/healthz", true},
		{"/readyz", true},
		{"/metrics", true},
		{"/api/channels", false},
		{"/api/messages/123/received", false},
		{"", false},
		{"/healthz/extra", false},
	}
	for _, tc := range cases {
		if got := shouldSkipEnvelope(tc.path); got != tc.want {
			t.Errorf("shouldSkipEnvelope(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

// TestWrapResponse_Success covers the 2xx branch: body is preserved verbatim
// inside the data field across object / array / null / primitive shapes.
func TestWrapResponse_Success(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     []byte
		wantData string
	}{
		{"object body", 200, []byte(`{"id":1}`), `{"id":1}`},
		{"array body", 201, []byte(`[1,2,3]`), `[1,2,3]`},
		{"empty body", 204, []byte(``), `null`},
		{"primitive body", 200, []byte(`"ok"`), `"ok"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := wrapResponse(tc.status, tc.body)
			var got struct {
				Status string          `json:"status"`
				Data   json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Status != "success" {
				t.Errorf("status = %q, want success", got.Status)
			}
			if string(got.Data) != tc.wantData {
				t.Errorf("data = %s, want %s", got.Data, tc.wantData)
			}
		})
	}
}

// TestWrapResponse_Error covers the non-2xx branch: error string is sourced
// from the body's `error` field when present, otherwise from HTTP status text.
func TestWrapResponse_Error(t *testing.T) {
	cases := []struct {
		name      string
		status    int
		body      []byte
		wantError string
	}{
		{"error field present", 404, []byte(`{"error":"channel not found"}`), "channel not found"},
		{"error field empty string", 422, []byte(`{"error":""}`), stdhttp.StatusText(422)},
		{"no error field", 500, []byte(`{"detail":"oops"}`), stdhttp.StatusText(500)},
		{"non-JSON body", 502, []byte(`bad gateway html`), stdhttp.StatusText(502)},
		{"empty body", 401, []byte(``), stdhttp.StatusText(401)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := wrapResponse(tc.status, tc.body)
			var got struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if got.Status != "error" {
				t.Errorf("status = %q, want error", got.Status)
			}
			if got.Error != tc.wantError {
				t.Errorf("error = %q, want %q", got.Error, tc.wantError)
			}
		})
	}
}

// TestEnvelopeWriter_DefaultStatus covers Write without an explicit
// WriteHeader: status is implicitly 200 (Go HTTP semantics).
func TestEnvelopeWriter_DefaultStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newGinWriter(rec)
	w := &envelopeWriter{ResponseWriter: gw, body: bytes.NewBuffer(nil)}

	if _, err := w.Write([]byte(`{"k":"v"}`)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if w.Status() != stdhttp.StatusOK {
		t.Errorf("Status = %d, want 200", w.Status())
	}
	if !w.Written() || w.Size() != len(`{"k":"v"}`) {
		t.Errorf("Written/Size mismatch: %v / %d", w.Written(), w.Size())
	}
}

// TestEnvelopeWriter_WriteString mirrors the Write path for the string
// variant; both should bump status from 0 to 200 on first call.
func TestEnvelopeWriter_WriteString(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newGinWriter(rec)
	w := &envelopeWriter{ResponseWriter: gw, body: bytes.NewBuffer(nil)}

	if _, err := w.WriteString(`hi`); err != nil {
		t.Fatalf("WriteString: %v", err)
	}
	if w.Status() != stdhttp.StatusOK {
		t.Errorf("Status = %d, want 200", w.Status())
	}
}

// TestEnvelopeWriter_ExplicitStatus covers WriteHeader: stored status takes
// precedence over the default in Status().
func TestEnvelopeWriter_ExplicitStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newGinWriter(rec)
	w := &envelopeWriter{ResponseWriter: gw, body: bytes.NewBuffer(nil)}

	w.WriteHeader(404)
	if w.Status() != 404 {
		t.Errorf("Status = %d, want 404", w.Status())
	}
	if w.Written() {
		t.Errorf("Written should be false before any Write")
	}
}

// TestEnvelopeWriter_StatusZeroDefault covers Status()'s defensive default
// when the wrapped writer never had a status set (no Write, no WriteHeader).
// In production this can happen if a handler returns without writing, and
// the middleware still needs a sane status to forward.
func TestEnvelopeWriter_StatusZeroDefault(t *testing.T) {
	rec := httptest.NewRecorder()
	gw := newGinWriter(rec)
	w := &envelopeWriter{ResponseWriter: gw, body: bytes.NewBuffer(nil)}

	if got := w.Status(); got != stdhttp.StatusOK {
		t.Errorf("Status on fresh writer = %d, want 200", got)
	}
}

// TestResponseEnvelope_EndToEnd wires a real gin engine + handler and
// asserts the wire-format the cses-client interceptor will see. This is
// the closest thing to a contract test for the envelope shape.
func TestResponseEnvelope_EndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(responseEnvelope())
	r.GET("/api/ok", func(c *gin.Context) {
		c.JSON(200, gin.H{"name": "alice"})
	})
	r.GET("/api/err", func(c *gin.Context) {
		c.JSON(404, gin.H{"error": "not found"})
	})
	r.GET("/healthz", func(c *gin.Context) {
		c.String(200, "ok")
	})

	t.Run("2xx wrapped as success", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/ok", nil)
		r.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("code = %d", rec.Code)
		}
		body := rec.Body.String()
		if body != `{"data":{"name":"alice"},"status":"success"}` &&
			body != `{"status":"success","data":{"name":"alice"}}` {
			t.Errorf("unexpected body: %s", body)
		}
	})

	t.Run("4xx wrapped as error with extracted message", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/err", nil)
		r.ServeHTTP(rec, req)
		if rec.Code != 404 {
			t.Errorf("code = %d", rec.Code)
		}
		var got map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got["status"] != "error" || got["error"] != "not found" {
			t.Errorf("unexpected envelope: %+v", got)
		}
	})

	t.Run("skipped path stays plain", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/healthz", nil)
		r.ServeHTTP(rec, req)
		if rec.Body.String() != "ok" {
			t.Errorf("healthz should not be wrapped, got %q", rec.Body.String())
		}
	})

	// Empty handler covers the defensive `if status == 0 { status = 200 }`
	// path inside responseEnvelope: nothing was written, the middleware
	// still needs to flush a wrapped 200/null response.
	r.GET("/api/empty", func(_ *gin.Context) {})
	t.Run("empty handler defaults to 200 + data:null", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/empty", nil)
		r.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("code = %d, want 200", rec.Code)
		}
		if rec.Body.String() != `{"data":null,"status":"success"}` &&
			rec.Body.String() != `{"status":"success","data":null}` {
			t.Errorf("unexpected envelope: %s", rec.Body.String())
		}
	})
}

// newGinWriter constructs the minimal gin.ResponseWriter needed to back an
// envelopeWriter in unit tests. Going through gin.CreateTestContext keeps
// the writer compatible with gin's interface evolution.
func newGinWriter(rec *httptest.ResponseRecorder) gin.ResponseWriter {
	c, _ := gin.CreateTestContext(rec)
	return c.Writer
}
