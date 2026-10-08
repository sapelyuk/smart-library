// Package events holds the broker agnostic event contracts of the platform:
// the envelope every domain event travels in (envelope.go), the transport ports
// declared here, and the environment configuration of the transport (config.go).
//
// The transport itself is RabbitMQ, as decided in ADR-0001
// (docs/adr/0001-message-broker.md): one topic exchange library.events, a
// routing key equal to the event type in the form <aggregate>.<action>, the
// JSON envelope in the message body and the envelope metadata mirrored into the
// message headers. This package deliberately knows nothing about AMQP: the
// broker driver lives in its own subpackage, so a service imports these ports
// and never a transport dependency.
//
// Delivery semantics fixed here (at-least-once, ADR-0001): a handler that
// returns nil acknowledges the message, a handler that returns an error rejects
// it without requeueing. A rejected message is not retried in place because a
// poisonous message would loop forever; the retry path is the dead-letter
// exchange of the queue it came from.
package events

import (
	"context"
	"time"
)

// Headers carries the metadata of a message next to its body. The keys are the
// Header* constants of this package for the envelope fields, plus anything a
// publisher finds worth mirroring.
//
// Values stay plain Go types (string, int32, int64, bool, time.Time) so a
// transport can serialize them without a conversion table.
type Headers map[string]any

// ContentType is the MIME type of the message body: the envelope is JSON.
const ContentType = "application/json"

// Publisher delivers the raw body of an event to the exchange under a routing
// key. Implementations are expected to be safe for concurrent use.
//
// The contract is transport level on purpose: it takes bytes and headers
// instead of a domain object, so a service can publish through it without
// leaking its types into the transport. Code that publishes domain events uses
// EventPublisher, which renders the envelope and then calls this interface.
type Publisher interface {
	// Publish hands the body over to the broker. A nil error means the broker
	// accepted the message, not that every consumer processed it.
	Publish(ctx context.Context, routingKey string, body []byte, headers Headers) error
}

// Message is one delivery handed to a handler.
type Message struct {
	// RoutingKey is the key the message was bound with, which for a domain
	// event is the event type.
	RoutingKey string

	// Body is the raw message body: the JSON envelope.
	Body []byte

	// Headers carries the metadata mirrored by the publisher.
	Headers Headers
}

// Handler processes one delivery.
//
// Returning nil acknowledges the message. Returning an error rejects it without
// requeueing: the message is not replayed in place, so a handler must be
// idempotent against the redeliveries the broker does produce (a connection
// lost before an ack), not against its own failures.
type Handler func(ctx context.Context, msg Message) error

// QueueConfig describes the queue a subscriber consumes from. Every field is
// expressible without naming a broker; how a broker realizes them is its own
// business.
type QueueConfig struct {
	// Name is the queue name. It must be stable across restarts of the
	// consumer: a name generated per process loses the messages that arrived
	// while the consumer was down.
	Name string

	// Bindings is the set of routing keys the queue is bound to. A domain
	// consumer lists the event types it handles, one per entry; wildcards such
	// as loan.* are allowed.
	Bindings []string

	// Durable keeps the queue and its messages across a broker restart. A
	// consumer of domain events almost always wants true: a restart of the
	// broker must not drop the backlog.
	//
	// A non-durable queue is a temporary one: RabbitMQ 4 rejects a transient
	// queue that is not exclusive, so the transport declares it exclusive and
	// the queue disappears with the connection that created it.
	Durable bool

	// MessageTTL delays delivery: a message that is not consumed within the
	// TTL is dead-lettered. Zero means no per-message timeout.
	//
	// This is the first half of the delayed delivery mechanism of ADR-0001: a
	// reminder is published into a queue whose TTL is the wait until it is due,
	// and whose DeadLetterExchange points at the queue the consumer actually
	// reads.
	MessageTTL time.Duration

	// DeadLetterExchange receives the messages MessageTTL rejected. Empty
	// means no dead-letter routing.
	DeadLetterExchange string

	// DeadLetterRoutingKey overrides the routing key of a dead-lettered
	// message. Empty means the original routing key is kept.
	DeadLetterRoutingKey string

	// PrefetchCount bounds how many unacknowledged messages the broker may hand
	// to this consumer at once. Zero means the transport default.
	PrefetchCount int
}

// Subscriber consumes the messages of a queue.
type Subscriber interface {
	// Subscribe binds the queue to the routing keys of cfg and starts
	// consuming. It blocks until ctx is canceled, then stops consuming and
	// returns. A handler error is reported through the returned error only for
	// the delivery that could not be processed; the subscription goes on.
	Subscribe(ctx context.Context, cfg QueueConfig, handler Handler) error
}
