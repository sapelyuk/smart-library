package consumer

import (
	"encoding/json"
	"testing"

	"github.com/sapelyuk/smart-library/pkg/events"
)

func TestDecodeBookEventEnvelope(t *testing.T) {
	const bookID = "9ee8f5da-7d51-49fb-9836-411198e48ef1"
	envelope, err := events.NewEvent(EventTypeBookCreated, BookEvent{BookID: bookID})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	got, err := decodeBookEvent(events.Message{RoutingKey: EventTypeBookCreated, Body: body})
	if err != nil {
		t.Fatalf("decodeBookEvent: %v", err)
	}
	if got.BookID != bookID {
		t.Fatalf("book_id = %q, want %q", got.BookID, bookID)
	}
}

func TestDecodeBookEventRejectsInvalidOrMismatchedEnvelope(t *testing.T) {
	tests := []struct {
		name       string
		routingKey string
		body       []byte
	}{
		{
			name:       "invalid envelope",
			routingKey: EventTypeBookCreated,
			body:       []byte(`{"book_id":"book-1"}`),
		},
		{
			name:       "routing key mismatch",
			routingKey: EventTypeBookCreated,
			body:       mustEventBody(t, EventTypeBookUpdated, BookEvent{BookID: "book-1"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeBookEvent(events.Message{RoutingKey: tt.routingKey, Body: tt.body}); err == nil {
				t.Fatal("decodeBookEvent() = nil error, want error")
			}
		})
	}
}

func mustEventBody(t *testing.T, eventType string, payload any) []byte {
	t.Helper()

	envelope, err := events.NewEvent(eventType, payload)
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}

	return body
}
