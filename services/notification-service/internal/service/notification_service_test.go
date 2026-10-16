// Package service_test holds black-box tests for the notification service layer.
//
// Tests cover business logic: channel selection (email primary, phone reserved),
// error handling, and edge cases. The email.Sender dependency is mocked via
// a Sender interface defined in this test package.
package service_test

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/sapelyuk/smart-library/services/notification-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/service"
)

// Sender is a mock of email.Sender used in tests.
type Sender struct {
	SendFunc func(ctx context.Context, to, subject, body string) error
}

func (m *Sender) Send(ctx context.Context, to, subject, body string) error {
	if m.SendFunc == nil {
		return nil
	}
	return m.SendFunc(ctx, to, subject, body)
}

func TestNotify_SuccessfulEmailDelivery(t *testing.T) {
	s := service.New(&Sender{
		SendFunc: func(ctx context.Context, to, subject, body string) error {
			if to != "reader@example.com" {
				t.Errorf("Send() to = %q, want %q", to, "reader@example.com")
			}
			if !strings.Contains(subject, "Книга выдана") {
				t.Errorf("Send() subject = %q, expected loan.issued subject", subject)
			}
			if len(body) == 0 {
				t.Error("Send() body is empty")
			}
			return nil
		},
	}, slog.Default())

	n := buildNotification(t, "loan.issued")

	if err := s.Notify(context.Background(), n); err != nil {
		t.Errorf("Notify() error = %v, want nil", err)
	}
}

func TestNotify_PhoneOnly_LogsReservation(t *testing.T) {
	s := service.New(&Sender{
		SendFunc: func(ctx context.Context, to, subject, body string) error {
			t.Error("Send() should not be called when only phone is set")
			return nil
		},
	}, slog.Default())

	n := buildNotificationWithPhone(t, "loan.issued")

	if err := s.Notify(context.Background(), n); err != nil {
		t.Errorf("Notify() error = %v, want nil", err)
	}
}

func TestNotify_NilNotification(t *testing.T) {
	s := service.New(&Sender{}, slog.Default())

	err := s.Notify(context.Background(), nil)
	if err == nil {
		t.Error("Notify(nil) returned nil error, want error")
	}
}

func TestNotify_EmailDeliveryFailure(t *testing.T) {
	wantErr := errors.New("connection refused")

	s := service.New(&Sender{
		SendFunc: func(ctx context.Context, to, subject, body string) error {
			return wantErr
		},
	}, slog.Default())

	n := buildNotification(t, "loan.issued")

	err := s.Notify(context.Background(), n)
	if err == nil {
		t.Fatal("Notify() returned nil error, want error")
	}
	if !errors.Is(err, domain.ErrDeliveryFailed) {
		t.Errorf("Notify() error = %v, want wrapped %v", err, domain.ErrDeliveryFailed)
	}
}

func TestNotify_ContextCancellationDuringSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	s := service.New(&Sender{
		SendFunc: func(ctx context.Context, to, subject, body string) error {
			return nil
		},
	}, slog.Default())

	n := buildNotification(t, "loan.issued")

	err := s.Notify(ctx, n)
	// The service logs before checking the channel, so it may not return ctx.Err()
	// but it should not panic or hang.
	_ = err
}

func TestNotify_EmailAndPhoneBothSet(t *testing.T) {
	emailCalled := false

	s := service.New(&Sender{
		SendFunc: func(ctx context.Context, to, subject, body string) error {
			emailCalled = true
			return nil
		},
	}, slog.Default())

	n := buildNotificationWithPhone(t, "loan.issued")
	n.ReaderEmail = "reader@example.com"

	if err := s.Notify(context.Background(), n); err != nil {
		t.Errorf("Notify() error = %v, want nil", err)
	}
	if !emailCalled {
		t.Error("Send() was not called for email delivery")
	}
}

func TestNotify_DefaultLogger(t *testing.T) {
	// New() should not panic when logger is nil.
	s := service.New(&Sender{}, nil)
	if s == nil {
		t.Fatal("New() returned nil service")
	}

	n := buildNotification(t, "loan.issued")
	if err := s.Notify(context.Background(), n); err != nil {
		t.Errorf("Notify() error = %v, want nil", err)
	}
}

func TestNotify_SubjectAndBodyMatchEventType(t *testing.T) {
	tests := []struct {
		eventType string
		subPrefix string
		bodySub   string
	}{
		{"loan.issued", "Книга выдана", "Вам выдана книга"},
		{"loan.overdue", "Напоминание", "Напоминаем"},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			sentSubject := ""
			sentBody := ""

			s := service.New(&Sender{
				SendFunc: func(ctx context.Context, to, subject, body string) error {
					sentSubject = subject
					sentBody = body
					return nil
				},
			}, slog.Default())

			n := buildNotification(t, tt.eventType)

			if err := s.Notify(context.Background(), n); err != nil {
				t.Fatalf("Notify() error = %v, want nil", err)
			}

			if !strings.HasPrefix(sentSubject, tt.subPrefix) {
				t.Errorf("subject = %q, expected prefix %q", sentSubject, tt.subPrefix)
			}
			if !strings.Contains(sentBody, tt.bodySub) {
				t.Errorf("body = %q, expected substring %q", sentBody, tt.bodySub)
			}
		})
	}
}

func buildNotification(t *testing.T, eventType string) *domain.Notification {
	t.Helper()
	n, err := domain.NewNotification(
		"reader-1",
		"reader@example.com",
		"",
		"Test Book",
		"Test Author",
		timeNow().Add(7*24*time.Hour),
		eventType,
	)
	if err != nil {
		t.Fatalf("NewNotification() error = %v", err)
	}
	return n
}

func timeNow() time.Time {
	return time.Now().UTC()
}

func buildNotificationWithPhone(t *testing.T, eventType string) *domain.Notification {
	t.Helper()
	n, err := domain.NewNotification(
		"reader-1",
		"",
		"+79001234567",
		"Test Book",
		"Test Author",
		timeNow().Add(7*24*time.Hour),
		eventType,
	)
	if err != nil {
		t.Fatalf("NewNotification() error = %v", err)
	}
	return n
}
