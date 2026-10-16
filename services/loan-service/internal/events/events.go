// Package events holds the domain event port of the loan service.
//
// The transport is RabbitMQ (ADR-0001, docs/adr/0001-message-broker.md): events
// are published to the topic exchange library.events under a routing key of the
// form <aggregate>.<action>. The service depends on the Publisher interface
// declared here, not on the broker, so the wiring in cmd/server injects the
// pkg/events implementation while the use cases stay transport agnostic.
package events

import (
	"context"
)

// Event types published by the loan service. The value is the routing key.
const (
	TypeLoanIssued   = "loan.issued"
	TypeLoanReturned = "loan.returned"
	TypeLoanOverdue  = "loan.overdue"
)

// Publisher delivers a domain event. Implementations must not fail the business
// operation that produced the event: a caller logs and moves on.
type Publisher interface {
	Publish(ctx context.Context, eventType string, payload any) error
}
