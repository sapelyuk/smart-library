package events

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// EventPublisher publishes domain events: it renders the payload into an
// envelope of the current schema version and hands the bytes to a transport
// Publisher.
//
// A service depends on this type (or on the narrow Publish method it exposes)
// rather than on a broker: the transport stays swappable and the business logic
// never sees AMQP.
type EventPublisher struct {
	publisher Publisher
}

// NewEventPublisher wraps a transport publisher. The publisher is required.
func NewEventPublisher(publisher Publisher) (*EventPublisher, error) {
	if publisher == nil {
		return nil, errors.New("events: publisher is required")
	}

	return &EventPublisher{publisher: publisher}, nil
}

// Publish renders payload into an envelope and sends it under eventType as the
// routing key. It returns an error when the event is invalid or the transport
// rejects it; a nil error means the broker accepted the message, not that every
// consumer processed it.
func (p *EventPublisher) Publish(ctx context.Context, eventType string, payload any) error {
	event, err := NewEvent(eventType, payload)
	if err != nil {
		return err
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("events: marshal envelope of %s: %w", eventType, err)
	}

	if err := p.publisher.Publish(ctx, event.RoutingKey(), body, event.Headers()); err != nil {
		return fmt.Errorf("events: publish %s: %w", eventType, err)
	}

	return nil
}
