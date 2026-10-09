// Package consumer keeps the book index in step with the library.
//
// book-service publishes book.created, book.updated and book.deleted whenever a
// book changes. This package subscribes to those events and re-indexes or removes
// the matching record, so an answer never cites a description that the library has
// already changed — or a book the library no longer has.
//
// Delivery is at-least-once, so every handler must be safe to run twice: indexing
// is keyed by book id and replaces what it wrote before, deleting is a no-op when
// nothing matches. A handler that returns an error makes the broker requeue the
// delivery to the dead-letter queue; a handler that returns nil acknowledges it.
package consumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/sapelyuk/smart-library/pkg/events"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
)

// Queue is the name of the queue this consumer owns.
const Queue = "ai.catalog.sync"

// EventTypeBookDeleted is the event that removes a book from the index. The other
// book events are declared here too so the bindings of this queue are visible in
// one place.
const (
	EventTypeBookCreated = "book.created"
	EventTypeBookUpdated = "book.updated"
	EventTypeBookDeleted = "book.deleted"
)

// BookEvent mirrors the payload book-service publishes. Only the id matters here:
// the book is read back rather than trusted from the event, because a slow consumer
// must not index a state older than the one already in the library.
type BookEvent struct {
	BookID string `json:"book_id"`
	ISBN   string `json:"isbn,omitempty"`
	Title  string `json:"title,omitempty"`
}

// Run consumes book events until ctx is canceled. The connection is expected to be
// open; the subscription retries internally and only gives up when the context ends,
// so a broker restart does not take the answers of the assistant down.
func Run(ctx context.Context, subscriber events.Subscriber, svc *service.Service, log *slog.Logger) error {
	if subscriber == nil {
		return errors.New("consumer: subscriber is required")
	}

	if svc == nil {
		return errors.New("consumer: service is required")
	}

	cfg := events.QueueConfig{
		Name: Queue,
		Bindings: []string{
			EventTypeBookCreated,
			EventTypeBookUpdated,
			EventTypeBookDeleted,
		},
		Durable:       true,
		PrefetchCount: 8,
	}

	log.Info("ai consumer: subscribing to catalog events",
		"queue", Queue,
		"bindings", cfg.Bindings,
	)

	return subscriber.Subscribe(ctx, cfg, handler(svc, log))
}

// handler is the callback the broker invokes per delivery.
func handler(svc *service.Service, log *slog.Logger) events.Handler {
	return func(ctx context.Context, msg events.Message) error {
		log := log.With(slog.String("event_type", msg.RoutingKey))

		var event BookEvent
		if err := json.Unmarshal(msg.Body, &event); err != nil {
			// A payload this service cannot read will not become readable by being
			// retried, so it goes straight to the dead-letter queue.
			log.Error("ai consumer: cannot parse event payload", "error", err)
			return fmt.Errorf("ai consumer: parse %s payload: %w", msg.RoutingKey, err)
		}

		if event.BookID == "" {
			log.Error("ai consumer: event carries no book id")
			return errors.New("ai consumer: event carries no book id")
		}

		log = log.With(slog.String("book_id", event.BookID))

		switch msg.RoutingKey {
		case EventTypeBookCreated, EventTypeBookUpdated:
			_, err := svc.SyncBook(ctx, event.BookID)
			if err != nil {
				log.Error("ai consumer: re-index book failed", "error", err)
				return fmt.Errorf("ai consumer: index book %s: %w", event.BookID, err)
			}

			log.Info("ai consumer: book indexed")

			return nil

		case EventTypeBookDeleted:
			deletion, err := svc.RemoveBook(ctx, event.BookID)
			if err != nil {
				log.Error("ai consumer: remove book failed", "error", err)
				return fmt.Errorf("ai consumer: remove book %s: %w", event.BookID, err)
			}

			// Zero is normal here: a delete re-delivered after the book was already
			// dropped has nothing left to remove.
			log.Info("ai consumer: book removed from index", "records_removed", deletion.Chunks)

			return nil

		default:
			// Bound queue but unknown routing key: acknowledge it, a binding added by
			// mistake must not fill the dead-letter queue forever.
			log.Warn("ai consumer: ignoring unexpected event type")

			return nil
		}
	}
}
