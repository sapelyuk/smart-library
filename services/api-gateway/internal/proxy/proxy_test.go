package proxy_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sapelyuk/smart-library/pkg/logger"
	"github.com/sapelyuk/smart-library/services/api-gateway/internal/proxy"
)

// newUpstream returns a httptest.Server that echoes back the request method,
// path, query, body and headers as a JSON object.
func newUpstream(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		headers := make(map[string]string)
		for k, v := range r.Header {
			headers[k] = strings.Join(v, ", ")
		}

		body, _ := io.ReadAll(r.Body)

		resp := map[string]any{
			"method":  r.Method,
			"path":    r.URL.Path,
			"rawQ":    r.URL.RawQuery,
			"headers": headers,
			"body":    string(body),
		}

		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}))
}

func newProxy(t *testing.T, target *url.URL) *proxy.Proxy {
	t.Helper()

	log, err := logger.New(logger.Options{Level: "error", Format: "text"})
	if err != nil {
		t.Fatalf("create logger: %v", err)
	}

	return proxy.New(target, log)
}

func TestProxy_Forwarding(t *testing.T) {
	t.Parallel()

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)
	rec := httptest.NewRecorder()

	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got, want := body["method"], "GET"; got != want {
		t.Errorf("method = %v, want %q", got, want)
	}

	if got, want := body["path"], "/v1/books"; got != want {
		t.Errorf("path = %v, want %q", got, want)
	}
}

func TestProxy_Methods(t *testing.T) {
	t.Parallel()

	testCases := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodDelete,
		http.MethodPatch,
	}

	for _, method := range testCases {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			upstream := newUpstream(t)
			target, _ := url.Parse(upstream.URL)
			p := newProxy(t, target)

			req := httptest.NewRequest(method, "/v1/test", http.NoBody)
			rec := httptest.NewRecorder()

			p.Handler().ServeHTTP(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}

			var body map[string]any
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			if got := body["method"]; got != method {
				t.Errorf("method = %v, want %q", got, method)
			}
		})
	}
}

func TestProxy_QueryStringPassthrough(t *testing.T) {
	t.Parallel()

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/v1/users?query=anna&role=ROLE_READER&limit=5", http.NoBody)
	rec := httptest.NewRecorder()

	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	rawQ := body["rawQ"].(string)
	for _, expected := range []string{"query=anna", "role=ROLE_READER", "limit=5"} {
		if !strings.Contains(rawQ, expected) {
			t.Errorf("query string %q not found in %q", expected, rawQ)
		}
	}
}

func TestProxy_BodyPassthrough(t *testing.T) {
	t.Parallel()

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	bodyStr := `{"email":"reader@example.com","full_name":"Anna Reader"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/users", strings.NewReader(bodyStr))
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got := resp["body"]; got != bodyStr {
		t.Errorf("body = %q, want %q", got, bodyStr)
	}
}

func TestProxy_HeadersForwarding(t *testing.T) {
	t.Parallel()

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodPost, "/v1/users", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-token-123")
	req.Header.Set("X-Custom", "custom-value")
	// Do NOT set X-Forwarded-For - the proxy should add it from RemoteAddr.

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	headers := body["headers"].(map[string]any)

	// Authorization must pass through unchanged.
	if got := headers["Authorization"]; got != "Bearer test-token-123" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer test-token-123")
	}

	// Custom header must pass through.
	if got := headers["X-Custom"]; got != "custom-value" {
		t.Errorf("X-Custom = %q, want %q", got, "custom-value")
	}

	// Content-Type must pass through.
	if got := headers["Content-Type"]; got != "application/json" {
		t.Errorf("Content-Type = %q, want %q", got, "application/json")
	}

	// X-Forwarded-For must be set by the proxy.
	xfw := headers["X-Forwarded-For"]
	if xfw == "" {
		t.Error("X-Forwarded-For is empty: the proxy did not set it")
	}
}

func TestProxy_XForwardedForNotOverwritten(t *testing.T) {
	t.Parallel()

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)
	req.Header.Set("X-Forwarded-For", "192.0.2.1")

	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	headers := body["headers"].(map[string]any)
	if got, _ := headers["X-Forwarded-For"].(string); !strings.Contains(got, "192.0.2.1") {
		t.Errorf("X-Forwarded-For = %q, want to contain %q (original value must be preserved)", got, "192.0.2.1")
	}
}

func TestProxy_UpstreamUnreachable(t *testing.T) {
	t.Parallel()

	// Point to a port that is not listening - the proxy must not panic and
	// should return a 502 Bad Gateway.
	target, _ := url.Parse("http://localhost:1")
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)
	rec := httptest.NewRecorder()

	// The standard library ReverseProxy returns 502 when the upstream
	// connection cannot be established.  We assert it does not panic and
	// returns a 5xx error code.
	p.Handler().ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Errorf("status = %d, expected an error status (upstream unreachable)", rec.Code)
	}

	if rec.Code < 500 {
		t.Errorf("status = %d, expected a 5xx error", rec.Code)
	}
}

func TestProxy_HealthEndpoint(t *testing.T) {
	t.Parallel()

	// The proxy layer itself does not own the /healthz endpoint - it is
	// defined in cmd/server/main.go.  This test confirms that a request
	// to /healthz forwarded through the proxy is treated like any other
	// path and forwarded to the upstream.  The upstream must handle the
	// path on its own.

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/healthz", http.NoBody)
	rec := httptest.NewRecorder()

	p.Handler().ServeHTTP(rec, req)

	// The proxy forwards the request; the upstream echoes back the path.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got := body["path"]; got != "/healthz" {
		t.Errorf("path = %v, want %q (proxy forwards /healthz unchanged)", got, "/healthz")
	}
}

func TestProxy_RouteToCorrectUpstream(t *testing.T) {
	t.Parallel()

	// Create two upstreams and verify the proxy forwards to the right one.
	bookUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"book"}`))
	}))
	defer bookUpstream.Close()

	userUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"service":"user"}`))
	}))
	defer userUpstream.Close()

	// Book proxy must forward to book-upstream.
	bookTarget, _ := url.Parse(bookUpstream.URL)
	bookP := newProxy(t, bookTarget)

	req := httptest.NewRequest(http.MethodGet, "/v1/books", http.NoBody)
	rec := httptest.NewRecorder()
	bookP.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("book proxy: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); !strings.Contains(got, `{"service":"book"}`) {
		t.Errorf("book proxy body = %q, want service=book", got)
	}

	// User proxy must forward to user-upstream.
	userTarget, _ := url.Parse(userUpstream.URL)
	userP := newProxy(t, userTarget)

	req = httptest.NewRequest(http.MethodGet, "/v1/users", http.NoBody)
	rec = httptest.NewRecorder()
	userP.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("user proxy: status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Body.String(); !strings.Contains(got, `{"service":"user"}`) {
		t.Errorf("user proxy body = %q, want service=user", got)
	}
}

func TestProxy_EmptyBody(t *testing.T) {
	t.Parallel()

	upstream := newUpstream(t)
	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/v1/books/123", http.NoBody)
	rec := httptest.NewRecorder()

	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if body["body"] != "" {
		t.Errorf("body = %q, want empty", body["body"])
	}
}

func TestProxy_StatusCodePassthrough(t *testing.T) {
	t.Parallel()

	// Upstream that returns a non-200 status.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not found"}`))
	}))
	defer upstream.Close()

	target, _ := url.Parse(upstream.URL)
	p := newProxy(t, target)

	req := httptest.NewRequest(http.MethodGet, "/v1/books/000", http.NoBody)
	rec := httptest.NewRecorder()

	p.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (must passthrough upstream status)", rec.Code, http.StatusNotFound)
	}

	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if got := body["error"]; got != "not found" {
		t.Errorf("error = %v, want %q", got, "not found")
	}
}
