// Package service holds the business logic of the notification service:
// it decides which channel to use and delegates delivery to the transport.
package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/sapelyuk/smart-library/services/notification-service/internal/domain"
)

// MailSender delivers a single email message. The concrete implementation
// lives in the email package; the interface keeps the service testable.
type MailSender interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Service delivers notifications to readers.
type Service struct {
	sender MailSender
	log    *slog.Logger
}

// New builds the notification service.
func New(sender MailSender, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}

	return &Service{sender: sender, log: log}
}

// Notify sends a reminder to the reader described by the notification.
// Email is the primary channel; phone delivery is reserved for a future
// SMS gateway and is currently only logged.
func (s *Service) Notify(ctx context.Context, n *domain.Notification) error {
	if n == nil {
		return fmt.Errorf("notification service: nil notification")
	}

	s.log.InfoContext(ctx, "notification ready",
		"event_type", n.EventType,
		"reader_id", n.ReaderID,
		"email", n.ReaderEmail,
		"book", n.BookTitle,
		"due_at", n.DueAt)

	if n.ReaderEmail != "" {
		if err := s.sender.Send(ctx, n.ReaderEmail, n.Subject(), n.Body()); err != nil {
			return fmt.Errorf("notification service: %w: %v", domain.ErrDeliveryFailed, err)
		}
	}

	if n.ReaderPhone != "" {
		s.log.InfoContext(ctx, "SMS delivery reserved for future gateway",
			"phone", n.ReaderPhone, "event_type", n.EventType)
	}

	return nil
}
