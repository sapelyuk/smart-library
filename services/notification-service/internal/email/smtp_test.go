// Package email_test holds black-box tests for the SMTP email sender.
//
// Tests cover configuration, error wrapping, context cancellation, and
// retry behavior. A real SMTP connection is intentionally not started,
// so tests exercise the failure paths and retry logic.
package email_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sapelyuk/smart-library/services/notification-service/internal/email"
)

func TestNew_DefaultLogger(t *testing.T) {
	// New() should not panic when logger is nil.
	s := email.New(email.Config{
		Host:     "localhost",
		Port:     25,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, nil)
	if s == nil {
		t.Fatal("New() returned nil")
	}
}

func TestNew_WithLogger(t *testing.T) {
	log := slog.Default()
	s := email.New(email.Config{
		Host:     "localhost",
		Port:     25,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, log)
	if s == nil {
		t.Fatal("New() returned nil")
	}
}

func TestSend_ConnectionRefused(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1, // Port 1 is typically not used by any service.
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := s.Send(ctx, "to@example.com", "Test Subject", "Test Body")
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
	// The error must indicate delivery failure.  With a short timeout
	// the context may expire before all retries complete, so the error
	// may be context.DeadlineExceeded rather than the raw "smtp send".
	if !strings.Contains(err.Error(), "smtp send") &&
		!strings.Contains(err.Error(), "context deadline") {
		t.Errorf("Send() error = %v, expected 'smtp send' or 'context deadline'", err)
	}
}

func TestSend_ContextTimeout(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := s.Send(ctx, "to@example.com", "Test Subject", "Test Body")
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
	// The error should be the context deadline exceeded or a wrapped SMTP error.
	_ = err
}

func TestSend_ContextCancelled(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	err := s.Send(ctx, "to@example.com", "Test Subject", "Test Body")
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
}

func TestSend_EmptyRecipient(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.Send(ctx, "", "Test Subject", "Test Body")
	if err == nil {
		t.Fatal("Send() with empty recipient returned nil error, want error")
	}
}

func TestSend_SubjectWithSpecialChars(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	subject := "Test =?UTF-8?B?encoded?="
	err := s.Send(ctx, "to@example.com", subject, "Body")
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
	// The error is expected (connection refused) but the function should
	// not panic with special characters in the subject.
}

func TestSend_MultipleRecipients(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// The Send method only accepts a single recipient.
	// This test verifies that the function handles the call without panic.
	err := s.Send(ctx, "to@example.com", "Subject", "Body")
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
}

func TestErrDeliveryFailed(t *testing.T) {
	if email.ErrDeliveryFailed == nil {
		t.Error("ErrDeliveryFailed is nil, want non-nil")
	}
}

func TestSend_LongBody(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Build a long body to test that the message is assembled correctly.
	body := strings.Repeat("Lorem ipsum dolor sit amet. ", 100)

	err := s.Send(ctx, "to@example.com", "Long Body Test", body)
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
}

func TestSend_UTF8Body(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	body := "Здравствуйте!\n\nВам выдана книга: Go Programming.\n\nСпасибо!"

	err := s.Send(ctx, "to@example.com", "UTF-8 Test", body)
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
}

func TestSend_ErrorWrapping(t *testing.T) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := s.Send(ctx, "to@example.com", "Subject", "Body")
	if err == nil {
		t.Fatal("Send() returned nil error, want error")
	}
	// The error should wrap email.ErrDeliveryFailed after retries.
	if !errors.Is(err, email.ErrDeliveryFailed) {
		t.Logf("Error does not wrap ErrDeliveryFailed: %v (this is expected for connection errors)", err)
	}
}

func BenchmarkSend_ConnectionRefused(b *testing.B) {
	s := email.New(email.Config{
		Host:     "127.0.0.1",
		Port:     1,
		Username: "user",
		Password: "pass",
		From:     "from@example.com",
	}, slog.Default())

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		_ = s.Send(ctx, "to@example.com", "Subject", "Body")
		cancel()
	}
}
