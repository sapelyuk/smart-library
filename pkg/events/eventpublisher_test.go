package events_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sapelyuk/smart-library/pkg/events"
)

// recordingPublisher captures what the EventPublisher hands to the transport.
type recordingPublisher struct {
	routingKey string
	body       []byte
	headers    events.Headers
	err        error
	calls      int
}

func (p *recordingPublisher) Publish(_ context.Context, routingKey string, body []byte, headers events.Headers) error {
	p.calls++
	p.routingKey = routingKey
	p.body = body
	p.headers = headers

	return p.err
}

func TestEventPublisherRendersEnvelope(t *testing.T) {
	transport := &recordingPublisher{}

	publisher, err := events.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	if err := publisher.Publish(context.Background(), "user.registered", testPayload{UserID: "u-1"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if transport.routingKey != "user.registered" {
		t.Errorf("routing key = %q, want user.registered", transport.routingKey)
	}

	event, err := events.DecodeEvent(transport.body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}

	if event.Type != "user.registered" {
		t.Errorf("event type = %q, want user.registered", event.Type)
	}

	if transport.headers[events.HeaderEventID] != event.ID.String() {
		t.Errorf("event id header = %v, want %s", transport.headers[events.HeaderEventID], event.ID)
	}
}

func TestEventPublisherRejectsInvalidEvent(t *testing.T) {
	transport := &recordingPublisher{}

	publisher, err := events.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	if err := publisher.Publish(context.Background(), "registered", testPayload{}); !errors.Is(err, events.ErrInvalidType) {
		t.Errorf("Publish error = %v, want ErrInvalidType", err)
	}

	if transport.calls != 0 {
		t.Errorf("transport calls = %d, want 0", transport.calls)
	}
}

func TestEventPublisherPropagatesTransportError(t *testing.T) {
	transport := &recordingPublisher{err: errors.New("broker is down")}

	publisher, err := events.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	if err := publisher.Publish(context.Background(), "user.registered", testPayload{}); err == nil {
		t.Fatal("Publish error = nil, want the transport error")
	}
}

func TestNewEventPublisherRequiresTransport(t *testing.T) {
	if _, err := events.NewEventPublisher(nil); err == nil {
		t.Error("NewEventPublisher(nil) = nil error, want an error")
	}
}
