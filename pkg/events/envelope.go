package events

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// SchemaVersion is the layout version of the envelope this package produces.
// Bump it only together with a migration note: a consumer rejects a version
// newer than it knows instead of guessing at fields it has never seen.
const SchemaVersion = 1

// Header keys mirroring the envelope fields next to the body, so a broker tool
// can route and debug a message without parsing JSON.
const (
	HeaderEventID       = "x-event-id"
	HeaderEventType     = "x-event-type"
	HeaderSchemaVersion = "x-schema-version"
	HeaderOccurredAt    = "x-occurred-at"
)

// Errors of the envelope contract.
var (
	// ErrInvalidType reports a routing key outside the <aggregate>.<action>
	// form ADR-0001 fixes.
	ErrInvalidType = errors.New("events: invalid event type")

	// ErrEmptyID reports an envelope without an event id.
	ErrEmptyID = errors.New("events: event id is empty")

	// ErrMissingOccurredAt reports an envelope without a timestamp.
	ErrMissingOccurredAt = errors.New("events: occurred_at is missing")

	// ErrMissingPayload reports an envelope without a payload. A null payload
	// is rejected as well: an event that carries nothing is a bug in the
	// producer, not a fact worth publishing.
	ErrMissingPayload = errors.New("events: payload is missing")

	// ErrUnsupportedSchema reports a schema version this package cannot read.
	ErrUnsupportedSchema = errors.New("events: unsupported schema version")
)

// eventTypePattern is the routing key grammar of ADR-0001: non-empty labels of
// lower case letters, digits, underscores and hyphens, separated by dots. The
// first label names the aggregate, the rest the action, so both book.borrowed
// and user.session.created are valid.
var eventTypePattern = regexp.MustCompile(`^[a-z0-9_-]+(\.[a-z0-9_-]+)+$`)

// Event is the envelope a domain event travels in.
//
// The JSON field names are part of the wire contract, fixed by ADR-0001:
//
//	{
//	  "event_id":       "550e8400-e29b-41d4-a716-446655440000",
//	  "event_type":     "loan.overdue",
//	  "occurred_at":    "2026-03-02T10:00:00Z",
//	  "schema_version": 1,
//	  "payload":        { "loan_id": "..." }
//	}
type Event struct {
	// ID is a version 4 UUID identifying this occurrence. It is the
	// idempotency key of a consumer: two deliveries of the same ID describe the
	// same fact, so a handler that already processed one ignores the other.
	ID uuid.UUID `json:"event_id"`

	// Type is the event type, which is also the routing key it is published
	// with.
	Type string `json:"event_type"`

	// OccurredAt is when the fact happened, in UTC. It is the time of the
	// domain event, not of the publish call: a message replayed a day later
	// still carries the original timestamp.
	OccurredAt time.Time `json:"occurred_at"`

	// SchemaVersion is the layout version of the payload this envelope was
	// written with.
	SchemaVersion int `json:"schema_version"`

	// Payload is the event data, kept raw so a consumer decodes it into the
	// type of its own choosing.
	Payload json.RawMessage `json:"payload"`
}

// NewEvent renders payload into an envelope of the current schema version, with
// a fresh version 4 id and the current time.
//
// A nil payload is rejected: see ErrMissingPayload.
func NewEvent(eventType string, payload any) (*Event, error) {
	if payload == nil {
		return nil, fmt.Errorf("%w: %s", ErrMissingPayload, eventType)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("events: marshal payload of %s: %w", eventType, err)
	}

	return newEvent(uuid.New(), eventType, time.Now().UTC(), raw)
}

// NewEventAt builds an envelope with an explicit id and timestamp, which is
// what a test or a replay of a stored event needs.
func NewEventAt(id uuid.UUID, eventType string, occurredAt time.Time, payload any) (*Event, error) {
	if payload == nil {
		return nil, fmt.Errorf("%w: %s", ErrMissingPayload, eventType)
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("events: marshal payload of %s: %w", eventType, err)
	}

	return newEvent(id, eventType, occurredAt.UTC(), raw)
}

func newEvent(id uuid.UUID, eventType string, occurredAt time.Time, payload json.RawMessage) (*Event, error) {
	event := &Event{
		ID:            id,
		Type:          eventType,
		OccurredAt:    occurredAt,
		SchemaVersion: SchemaVersion,
		Payload:       payload,
	}

	if err := event.Validate(); err != nil {
		return nil, err
	}

	return event, nil
}

// RoutingKey returns the key the event is published with, which ADR-0001
// defines as the event type itself.
func (e *Event) RoutingKey() string { return e.Type }

// Validate reports whether the envelope is publishable.
func (e *Event) Validate() error {
	if !ValidateType(e.Type) {
		return fmt.Errorf("%w: %q, want <aggregate>.<action>", ErrInvalidType, e.Type)
	}

	if e.ID == uuid.Nil {
		return fmt.Errorf("%w: %s", ErrEmptyID, e.Type)
	}

	if e.OccurredAt.IsZero() {
		return fmt.Errorf("%w: %s", ErrMissingOccurredAt, e.Type)
	}

	if len(bytes.TrimSpace(e.Payload)) == 0 || bytes.Equal(bytes.TrimSpace(e.Payload), []byte("null")) {
		return fmt.Errorf("%w: %s", ErrMissingPayload, e.Type)
	}

	if e.SchemaVersion > SchemaVersion {
		return fmt.Errorf("%w: %d, this build reads up to %d", ErrUnsupportedSchema, e.SchemaVersion, SchemaVersion)
	}

	return nil
}

// MarshalJSON renders the envelope, normalizing the timestamp to UTC so a
// consumer never has to guess the offset it is reading.
func (e *Event) MarshalJSON() ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}

	type wire struct {
		ID            uuid.UUID       `json:"event_id"`
		Type          string          `json:"event_type"`
		OccurredAt    time.Time       `json:"occurred_at"`
		SchemaVersion int             `json:"schema_version"`
		Payload       json.RawMessage `json:"payload"`
	}

	return json.Marshal(wire{
		ID:            e.ID,
		Type:          e.Type,
		OccurredAt:    e.OccurredAt.UTC(),
		SchemaVersion: e.SchemaVersion,
		Payload:       e.Payload,
	})
}

// Headers returns the envelope metadata in the form the transport mirrors into
// the message headers.
func (e *Event) Headers() Headers {
	return Headers{
		HeaderEventID:       e.ID.String(),
		HeaderEventType:     e.Type,
		HeaderSchemaVersion: int32(e.SchemaVersion),
		HeaderOccurredAt:    e.OccurredAt.UTC().Format(time.RFC3339Nano),
	}
}

// DecodeEvent reads an envelope from its JSON representation and validates it.
func DecodeEvent(raw []byte) (*Event, error) {
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, fmt.Errorf("events: decode envelope: %w", err)
	}

	// A producer that predates versioning leaves the field out. Reading an
	// unknown layout as version 1 would be a guess, so it is rejected.
	if event.SchemaVersion == 0 {
		return nil, fmt.Errorf("%w: field schema_version is absent", ErrUnsupportedSchema)
	}

	if err := event.Validate(); err != nil {
		return nil, err
	}

	return &event, nil
}

// ValidateType reports whether eventType is a routing key in the
// <aggregate>.<action> grammar of ADR-0001.
func ValidateType(eventType string) bool {
	return eventTypePattern.MatchString(eventType)
}
