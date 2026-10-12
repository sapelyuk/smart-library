// Package consumer subscribes to the loan.* domain events from RabbitMQ and
// feeds the notification service.
//
// It binds a durable queue to the platform exchange with binding key loan.#
// so it receives every loan event (issued, returned, overdue). Each message
// is decoded from the envelope, mapped to a domain notification and handed to
// the service for delivery.
package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/sapelyuk/smart-library/pkg/events"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/service"
)

// QueueName is the durable queue the notification service consumes from.
const QueueName = "notification-service.loan-events"

// loanIssuedPayload mirrors the payload of a loan.issued event.
type loanIssuedPayload struct {
	LoanID      string    `json:"loan_id"`
	ReaderID    string    `json:"reader_id"`
	BookID      string    `json:"book_id"`
	CopyID      string    `json:"copy_id"`
	BorrowedAt  time.Time `json:"borrowed_at"`
	DueAt       time.Time `json:"due_at"`
	BookTitle   string    `json:"book_title"`
	BookAuthor  string    `json:"book_author"`
	ReaderEmail string    `json:"reader_email"`
	ReaderPhone string    `json:"reader_phone"`
}

// loanOverduePayload mirrors the payload of a loan.overdue event.
type loanOverduePayload struct {
	LoanID      string    `json:"loan_id"`
	ReaderID    string    `json:"reader_id"`
	BookID      string    `json:"book_id"`
	DueAt       time.Time `json:"due_at"`
	BookTitle   string    `json:"book_title"`
	BookAuthor  string    `json:"book_author"`
	ReaderEmail string    `json:"reader_email"`
	ReaderPhone string    `json:"reader_phone"`
}

// Run subscribes to loan.* events and processes them until ctx is canceled.
// The subscription reconnects on its own (the amqp transport handles that),
// so this function blocks until the context is done.
func Run(ctx context.Context, subscriber events.Subscriber, svc *service.Service, log *slog.Logger) {
	cfg := events.QueueConfig{
		Name:          QueueName,
		Bindings:      []string{"loan.#"},
		Durable:       true,
		PrefetchCount: 10,
	}

	err := subscriber.Subscribe(ctx, cfg, func(ctx context.Context, msg events.Message) error {
		event, err := events.DecodeEvent(msg.Body)
		if err != nil {
			// Rejecting malformed messages keeps poison pills out of the queue.
			return fmt.Errorf("consumer: decode envelope: %w", err)
		}

		notification, err := buildNotification(event)
		if err != nil {
			// A valid envelope with an unknown type or missing fields is
			// rejected without requeue — it will not fix itself on retry.
			return fmt.Errorf("consumer: build notification: %w", err)
		}

		if err := svc.Notify(ctx, notification); err != nil {
			// Returning an error rejects the message (nack without requeue)
			// so the dead-letter exchange can collect failures.
			return fmt.Errorf("consumer: notify: %w", err)
		}

		return nil
	})
	if err != nil {
		log.Warn("notification consumer stopped", "error", err)
	}
}

// buildNotification maps an event envelope to a domain notification.
func buildNotification(event *events.Event) (*domain.Notification, error) {
	switch event.Type {
	case "loan.issued":
		var payload loanIssuedPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode loan.issued payload: %w", err)
		}

		return domain.NewNotification(
			payload.ReaderID,
			payload.ReaderEmail,
			payload.ReaderPhone,
			payload.BookTitle,
			payload.BookAuthor,
			payload.DueAt,
			event.Type,
		)

	case "loan.overdue":
		var payload loanOverduePayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode loan.overdue payload: %w", err)
		}

		return domain.NewNotification(
			payload.ReaderID,
			payload.ReaderEmail,
			payload.ReaderPhone,
			payload.BookTitle,
			payload.BookAuthor,
			payload.DueAt,
			event.Type,
		)

	default:
		return nil, fmt.Errorf("unhandled event type: %s", event.Type)
	}
}
