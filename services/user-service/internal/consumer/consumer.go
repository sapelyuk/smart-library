// Package consumer holds the placeholder notification consumer of the platform.
//
// Real notifications (welcome messages, due-date reminders) belong to the
// dedicated Notification Service (issue #48). Until it exists, this stub keeps
// the consumer path of pkg/events exercised end to end: it binds a durable queue
// to the user events, decodes the envelope and logs the fact, acknowledging each
// message. Replacing it with the real service does not touch the producers.
package consumer

import (
	"context"
	"log/slog"

	"github.com/sapelyuk/smart-library/pkg/events"
)

// NotificationStubQueue is the queue the placeholder consumer reads from.
const NotificationStubQueue = "user.notifications.stub"

// RunNotificationStub consumes the user events until ctx is canceled. It blocks
// (the subscription reconnects on its own), so the composition root runs it in a
// goroutine.
func RunNotificationStub(ctx context.Context, subscriber events.Subscriber, log *slog.Logger) {
	cfg := events.QueueConfig{
		Name:          NotificationStubQueue,
		Bindings:      []string{"user.#"},
		Durable:       true,
		PrefetchCount: 10,
	}

	err := subscriber.Subscribe(ctx, cfg, func(ctx context.Context, msg events.Message) error {
		event, err := events.DecodeEvent(msg.Body)
		if err != nil {
			// Rejecting the message (the handler returns an error) keeps a
			// malformed delivery out of the queue instead of looping on it.
			return err
		}

		log.InfoContext(ctx, "notification stub received an event",
			"event_type", event.Type,
			"event_id", event.ID.String(),
			"routing_key", msg.RoutingKey)

		return nil
	})
	if err != nil {
		log.Warn("notification stub stopped", "error", err)
	}
}
