// Package handler_test holds black-box tests for the gRPC handler.
//
// Tests cover the Health RPC, the REST gateway setup, and the localTarget
// utility that resolves wildcard listen addresses.
package handler_test

import (
	"context"
	"net/http"
	"testing"

	notificationv1 "github.com/sapelyuk/smart-library/services/notification-service/gen/go/notification/v1"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/handler"
)

func TestGRPCServer_Health(t *testing.T) {
	srv := handler.NewGRPCServer(nil)

	resp, err := srv.Health(context.Background(), &notificationv1.Empty{})
	if err != nil {
		t.Fatalf("Health() error = %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("Health() returned nil response")
	}
}

func TestGRPCServer_Health_NilLogger(t *testing.T) {
	// NewGRPCServer should not panic when logger is nil.
	srv := handler.NewGRPCServer(nil)
	if srv == nil {
		t.Fatal("NewGRPCServer() returned nil")
	}

	_, err := srv.Health(context.Background(), &notificationv1.Empty{})
	if err != nil {
		t.Errorf("Health() error = %v, want nil", err)
	}
}

func TestNewREST_HealthzEndpoint(t *testing.T) {
	mux, err := handler.NewREST("127.0.0.1:8084")
	if err != nil {
		t.Fatalf("NewREST() error = %v, want nil", err)
	}
	if mux == nil {
		t.Fatal("NewREST() returned nil handler")
	}

	req, err := http.NewRequest(http.MethodGet, "/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	rec := &recorder{}
	mux.ServeHTTP(rec, req)

	if rec.code != http.StatusOK {
		t.Errorf("/healthz status = %d, want %d", rec.code, http.StatusOK)
	}
	if rec.body != "OK" {
		t.Errorf("/healthz body = %q, want %q", rec.body, "OK")
	}
}

func TestNewREST_EmptyGRPCAddr(t *testing.T) {
	// grpc.NewClient is lazy: it does not validate the target at construction
	// time, so an empty address does not return an error here.
	mux, err := handler.NewREST("")
	if err != nil {
		t.Fatalf("NewREST(\"\") unexpected error: %v", err)
	}
	if mux == nil {
		t.Fatal("NewREST(\"\") returned nil handler")
	}
}

func TestNewREST_UnreachableGRPCAddr(t *testing.T) {
	// grpc.NewClient is lazy: an unreachable address does not fail at
	// construction time. The handler is still built; RPC calls would fail.
	mux, err := handler.NewREST("invalid-grpc-host:99999")
	if err != nil {
		t.Fatalf("NewREST(unreachable) unexpected error: %v", err)
	}
	if mux == nil {
		t.Fatal("NewREST(unreachable) returned nil handler")
	}
}

func TestNewREST_CustomPathNotHealthz(t *testing.T) {
	mux, err := handler.NewREST("127.0.0.1:8084")
	if err != nil {
		t.Fatalf("NewREST() error = %v", err)
	}

	req, err := http.NewRequest(http.MethodGet, "/some-other-path", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	rec := &recorder{}
	mux.ServeHTTP(rec, req)

	// The grpc-gateway returns 404 for unregistered paths.
	if rec.code == http.StatusOK {
		t.Errorf("/some-other-path status = %d, expected non-200", rec.code)
	}
}

func TestNewREST_WildcardAddress(t *testing.T) {
	// Addresses like ":8084" are converted to "127.0.0.1:8084" by
	// localTarget so they can be dialed from localhost.
	mux, err := handler.NewREST(":8085")
	if err != nil {
		t.Fatalf("NewREST() error = %v, want nil", err)
	}
	if mux == nil {
		t.Fatal("NewREST() returned nil handler")
	}

	req, err := http.NewRequest(http.MethodGet, "/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	rec := &recorder{}
	mux.ServeHTTP(rec, req)

	if rec.code != http.StatusOK {
		t.Errorf("/healthz status = %d, want %d", rec.code, http.StatusOK)
	}
}

func TestNewREST_IPv6Address(t *testing.T) {
	mux, err := handler.NewREST("[::]:8086")
	if err != nil {
		t.Fatalf("NewREST() error = %v, want nil", err)
	}

	req, err := http.NewRequest(http.MethodGet, "/healthz", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}

	rec := &recorder{}
	mux.ServeHTTP(rec, req)

	if rec.code != http.StatusOK {
		t.Errorf("/healthz status = %d, want %d", rec.code, http.StatusOK)
	}
}

func BenchmarkGRPCServer_Health(b *testing.B) {
	srv := handler.NewGRPCServer(nil)
	req := &notificationv1.Empty{}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := srv.Health(context.Background(), req)
		if err != nil {
			b.Fatalf("Health() error = %v", err)
		}
	}
}

// recorder is a minimal http.ResponseWriter that captures the status code and body.
type recorder struct {
	code int
	body string
}

func (r *recorder) Header() http.Header {
	return http.Header{}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.body = string(p)
	if r.code == 0 {
		r.code = http.StatusOK
	}
	return len(p), nil
}

func (r *recorder) WriteHeader(code int) {
	r.code = code
}
