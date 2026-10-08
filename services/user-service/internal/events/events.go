// Package events holds the domain event port of the user service.
//
// The transport is RabbitMQ (ADR-0001, docs/adr/0001-message-broker.md): events
// are published to the topic exchange library.events under a routing key of the
// form <aggregate>.<action>. The service depends on the Publisher interface
// declared here, not on the broker, so the wiring in cmd/server injects the
// pkg/events implementation while the use cases stay transport agnostic.
package events

import (
	"context"
	"time"
)

// Event types published by the user service. The value is the routing key.
const (
	// TypeUserRegistered reports that an account was created, either by the
	// reader themselves or by a librarian.
	TypeUserRegistered = "user.registered"

	// TypeUserStatusChanged reports that an account was activated or blocked.
	TypeUserStatusChanged = "user.status.changed"

	// TypeUserSessionCreated reports that a sign in issued a session.
	TypeUserSessionCreated = "user.session.created"
)

// Publisher delivers a domain event. Implementations must not fail the business
// operation that produced the event: a caller logs and moves on.
type Publisher interface {
	Publish(ctx context.Context, eventType string, payload any) error
}

// UserPayload describes the account a user event is about. It never carries the
// password hash.
type UserPayload struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

// SessionPayload describes a session that was created.
type SessionPayload struct {
	UserID    string    `json:"user_id"`
	SessionID string    `json:"session_id"`
	ExpiresAt time.Time `json:"expires_at"`
}
