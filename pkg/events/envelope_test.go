package events_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/pkg/events"
)

type testPayload struct {
	UserID string `json:"user_id"`
}

func TestNewEventBuildsEnvelope(t *testing.T) {
	before := time.Now().UTC()

	event, err := events.NewEvent("user.registered", testPayload{UserID: "u-1"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	if event.Type != "user.registered" {
		t.Errorf("Type = %q, want user.registered", event.Type)
	}

	if event.ID == uuid.Nil {
		t.Error("ID is nil, want a generated uuid")
	}

	if event.SchemaVersion != events.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", event.SchemaVersion, events.SchemaVersion)
	}

	if event.OccurredAt.Before(before) || event.OccurredAt.After(time.Now().UTC()) {
		t.Errorf("OccurredAt = %s, want a time around now", event.OccurredAt)
	}

	if event.RoutingKey() != "user.registered" {
		t.Errorf("RoutingKey = %q, want the event type", event.RoutingKey())
	}

	var payload testPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}

	if payload.UserID != "u-1" {
		t.Errorf("payload.UserID = %q, want u-1", payload.UserID)
	}
}

func TestNewEventRejectsBadInput(t *testing.T) {
	tests := []struct {
		name      string
		eventType string
		payload   any
		want      error
	}{
		{"nil payload", "user.registered", nil, events.ErrMissingPayload},
		{"empty type", "", testPayload{}, events.ErrInvalidType},
		{"single label", "registered", testPayload{}, events.ErrInvalidType},
		{"upper case", "User.Registered", testPayload{}, events.ErrInvalidType},
		{"trailing dot", "user.", testPayload{}, events.ErrInvalidType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := events.NewEvent(tt.eventType, tt.payload)
			if !errors.Is(err, tt.want) {
				t.Fatalf("NewEvent error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestNewEventAtKeepsIDAndTime(t *testing.T) {
	id := uuid.New()
	occurredAt := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)

	event, err := events.NewEventAt(id, "loan.overdue", occurredAt, testPayload{UserID: "u-2"})
	if err != nil {
		t.Fatalf("NewEventAt: %v", err)
	}

	if event.ID != id {
		t.Errorf("ID = %s, want %s", event.ID, id)
	}

	if !event.OccurredAt.Equal(occurredAt) {
		t.Errorf("OccurredAt = %s, want %s", event.OccurredAt, occurredAt)
	}

	if _, err := events.NewEventAt(uuid.Nil, "loan.overdue", occurredAt, testPayload{}); !errors.Is(err, events.ErrEmptyID) {
		t.Errorf("NewEventAt(nil id) error = %v, want ErrEmptyID", err)
	}
}

func TestMarshalJSONNormalizesTime(t *testing.T) {
	zone := time.FixedZone("MSK", 3*60*60)
	occurredAt := time.Date(2026, 3, 2, 13, 0, 0, 0, zone)

	event, err := events.NewEventAt(uuid.New(), "user.session.created", occurredAt, testPayload{UserID: "u-3"})
	if err != nil {
		t.Fatalf("NewEventAt: %v", err)
	}

	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	var decoded struct {
		OccurredAt time.Time `json:"occurred_at"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.OccurredAt.Location() != time.UTC {
		t.Errorf("occurred_at location = %s, want UTC", decoded.OccurredAt.Location())
	}

	if !decoded.OccurredAt.Equal(occurredAt) {
		t.Errorf("occurred_at = %s, want %s", decoded.OccurredAt, occurredAt)
	}
}

func TestDecodeEventRoundTrip(t *testing.T) {
	original, err := events.NewEvent("user.status.changed", testPayload{UserID: "u-4"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}

	decoded, err := events.DecodeEvent(raw)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("ID = %s, want %s", decoded.ID, original.ID)
	}

	if decoded.Type != original.Type {
		t.Errorf("Type = %q, want %q", decoded.Type, original.Type)
	}

	if !decoded.OccurredAt.Equal(original.OccurredAt) {
		t.Errorf("OccurredAt = %s, want %s", decoded.OccurredAt, original.OccurredAt)
	}

	if string(decoded.Payload) != string(original.Payload) {
		t.Errorf("Payload = %s, want %s", decoded.Payload, original.Payload)
	}
}

func TestDecodeEventRejectsBadEnvelopes(t *testing.T) {
	valid := `{"event_id":"550e8400-e29b-41d4-a716-446655440000","event_type":"user.registered","occurred_at":"2026-03-02T10:00:00Z","schema_version":1,"payload":{"user_id":"u"}}`

	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"not json", `{`, nil},
		{"missing schema version", strings.Replace(valid, `"schema_version":1,`, "", 1), events.ErrUnsupportedSchema},
		{"newer schema version", strings.Replace(valid, `"schema_version":1`, `"schema_version":99`, 1), events.ErrUnsupportedSchema},
		{"missing payload", strings.Replace(valid, `"payload":{"user_id":"u"}`, `"payload":null`, 1), events.ErrMissingPayload},
		{"bad type", strings.Replace(valid, `"user.registered"`, `"registered"`, 1), events.ErrInvalidType},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := events.DecodeEvent([]byte(tt.raw))
			if tt.want == nil {
				if err == nil {
					t.Fatal("DecodeEvent error = nil, want an error")
				}

				return
			}

			if !errors.Is(err, tt.want) {
				t.Fatalf("DecodeEvent error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestHeadersMirrorEnvelope(t *testing.T) {
	event, err := events.NewEvent("user.registered", testPayload{UserID: "u-5"})
	if err != nil {
		t.Fatalf("NewEvent: %v", err)
	}

	headers := event.Headers()

	if headers[events.HeaderEventID] != event.ID.String() {
		t.Errorf("event id header = %v, want %s", headers[events.HeaderEventID], event.ID)
	}

	if headers[events.HeaderEventType] != "user.registered" {
		t.Errorf("event type header = %v, want user.registered", headers[events.HeaderEventType])
	}

	if headers[events.HeaderSchemaVersion] != int32(events.SchemaVersion) {
		t.Errorf("schema header = %v, want %d", headers[events.HeaderSchemaVersion], events.SchemaVersion)
	}

	if _, ok := headers[events.HeaderOccurredAt].(string); !ok {
		t.Errorf("occurred_at header = %v, want a string", headers[events.HeaderOccurredAt])
	}
}

func TestValidateType(t *testing.T) {
	valid := []string{"user.registered", "loan.overdue", "book.borrowed", "user.session.created", "a1.b-2_c"}
	for _, key := range valid {
		if !events.ValidateType(key) {
			t.Errorf("ValidateType(%q) = false, want true", key)
		}
	}

	invalid := []string{"", "registered", ".registered", "user.", "user..registered", "User.registered", "user.registered!"}
	for _, key := range invalid {
		if events.ValidateType(key) {
			t.Errorf("ValidateType(%q) = true, want false", key)
		}
	}
}
