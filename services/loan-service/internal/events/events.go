// Package events holds the domain event port of the loan service.
//
// The real transport is RabbitMQ (ADR-0001, docs/adr/0001-message-broker.md):
// events are published to the topic exchange library.events under a routing key
// of the form <aggregate>.<action>. The publisher itself is part of issue #14
// (pkg/events); until it lands, LogPublisher records the events in the log so
// the wiring, the event names and the payloads are already fixed and the
// transport can be swapped without touching the business logic.
package events

import (
	"context"
	"log/slog"
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

// LogPublisher is the stand-in publisher used until pkg/events (#14) provides
// the RabbitMQ one. It keeps the envelope and the event names visible in the
// service log.
type LogPublisher struct {
	log *slog.Logger
}

// NewLogPublisher builds a publisher that writes events to the logger.
func NewLogPublisher(log *slog.Logger) *LogPublisher {
	return &LogPublisher{log: log}
}

// Publish records the event. It never fails, so callers can treat a publish
// error as best effort.
func (p *LogPublisher) Publish(ctx context.Context, eventType string, payload any) error {
	p.log.InfoContext(ctx, "domain event published", "event_type", eventType, "payload", payload)

	return nil
}
