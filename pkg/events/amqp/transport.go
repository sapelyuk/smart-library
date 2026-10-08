// Package amqp implements the events transport ports of pkg/events on top of
// RabbitMQ, the broker chosen in ADR-0001 (docs/adr/0001-message-broker.md).
//
// One topic exchange carries every domain event, a routing key is the event
// type, and the message body is the JSON envelope produced by pkg/events. The
// implementation follows the delivery guarantees of the ADR: publisher
// confirms on the producer side, manual acknowledgement on the consumer side,
// persistent messages, and delayed delivery through a message TTL plus a
// dead-letter exchange.
//
// The transport is deliberately forgiving about the broker being down: it
// connects lazily, retries a publish once after reconnecting, and a
// subscription reconnects in a loop until its context is canceled. Events are
// therefore best effort, which is what the services expect from the async path.
package amqp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/sapelyuk/smart-library/pkg/events"
)

// Defaults of the transport behaviour.
const (
	// ConfirmTimeout bounds how long a publish waits for the broker to confirm
	// it before giving up.
	ConfirmTimeout = 5 * time.Second

	// ReconnectDelay is the pause between two attempts to restore a lost
	// subscription.
	ReconnectDelay = 2 * time.Second
)

// Transport is a RabbitMQ implementation of both event ports. It is safe for
// concurrent use: the publishing side shares one connection guarded by a mutex,
// while every subscription owns its own connection.
type Transport struct {
	cfg events.Config
	log *slog.Logger

	// publishMu serialises the publish-confirm sequence, so a confirmation read
	// from the shared channel always belongs to the publish in flight.
	publishMu sync.Mutex

	mu       sync.Mutex
	conn     *amqp091.Connection
	ch       *amqp091.Channel
	confirms chan amqp091.Confirmation
}

// New builds a transport. It does not open a connection: the broker is dialed
// on the first publish or subscription, so a service starts even while the
// broker is unavailable.
func New(cfg events.Config, log *slog.Logger) (*Transport, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if log == nil {
		log = slog.Default()
	}

	return &Transport{cfg: cfg, log: log}, nil
}

// Publish sends body under routingKey with headers, waiting for the broker to
// confirm the message. A connection lost mid-publish is retried once on a fresh
// channel.
func (t *Transport) Publish(ctx context.Context, routingKey string, body []byte, headers events.Headers) error {
	t.publishMu.Lock()
	defer t.publishMu.Unlock()

	err := t.publishOnce(ctx, routingKey, body, headers)
	if err == nil {
		return nil
	}

	// The most likely cause is a stale channel, so the next attempt starts from
	// a clean connection.
	t.reset()

	if retryErr := t.publishOnce(ctx, routingKey, body, headers); retryErr != nil {
		return errors.Join(err, retryErr)
	}

	return nil
}

func (t *Transport) publishOnce(ctx context.Context, routingKey string, body []byte, headers events.Headers) error {
	ch, confirms, err := t.channel()
	if err != nil {
		return err
	}

	message := amqp091.Publishing{
		ContentType:  events.ContentType,
		DeliveryMode: amqp091.Persistent,
		Timestamp:    time.Now().UTC(),
		Type:         routingKey,
		Headers:      amqp091.Table(headers),
		Body:         body,
	}

	if err := ch.PublishWithContext(ctx, t.cfg.Exchange, routingKey, false, false, message); err != nil {
		return fmt.Errorf("amqp: publish %s: %w", routingKey, err)
	}

	select {
	case confirmation, ok := <-confirms:
		if !ok {
			return errors.New("amqp: confirm channel closed before acknowledgement")
		}

		if !confirmation.Ack {
			return fmt.Errorf("amqp: broker rejected message %s", routingKey)
		}

		return nil
	case <-time.After(ConfirmTimeout):
		return fmt.Errorf("amqp: confirm timeout for %s", routingKey)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Subscribe declares the queue of cfg, binds it to the routing keys and
// consumes until ctx is canceled. A lost connection or a broker restart is
// survived: the subscription is re-established after ReconnectDelay.
//
// It returns nil when ctx is canceled (a clean shutdown) and never returns
// while the context is alive, so the caller runs it in a goroutine.
func (t *Transport) Subscribe(ctx context.Context, cfg events.QueueConfig, handler events.Handler) error {
	if cfg.Name == "" {
		return errors.New("amqp: queue name is required")
	}

	if handler == nil {
		return errors.New("amqp: handler is required")
	}

	for {
		err := t.consume(ctx, cfg, handler)
		if ctx.Err() != nil {
			return nil
		}

		t.log.Warn("amqp: subscription lost, reconnecting",
			"queue", cfg.Name, "error", err)

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(ReconnectDelay):
		}
	}
}

// Close releases the publishing connection. A subscription owns its connection
// and is stopped by canceling the context of Subscribe.
func (t *Transport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	var err error

	if t.ch != nil {
		err = t.ch.Close()
		t.ch = nil
	}

	if t.conn != nil {
		if closeErr := t.conn.Close(); err == nil {
			err = closeErr
		}

		t.conn = nil
	}

	t.confirms = nil

	return err
}

// consume runs one subscription until its connection breaks or ctx is canceled.
func (t *Transport) consume(ctx context.Context, cfg events.QueueConfig, handler events.Handler) error {
	conn, err := amqp091.Dial(t.cfg.URL)
	if err != nil {
		return fmt.Errorf("amqp: dial: %w", err)
	}

	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("amqp: open channel: %w", err)
	}

	defer ch.Close()

	if err := declareExchange(ch, t.cfg.Exchange); err != nil {
		return err
	}

	// RabbitMQ 4 no longer permits a transient (non-durable) queue that is not
	// exclusive, so a non-durable queue is declared exclusive: it lives for the
	// lifetime of the connection, which is what a temporary queue wants anyway.
	queue, err := ch.QueueDeclare(cfg.Name, cfg.Durable, false, !cfg.Durable, false, queueArguments(cfg))
	if err != nil {
		return fmt.Errorf("amqp: declare queue %s: %w", cfg.Name, err)
	}

	for _, binding := range cfg.Bindings {
		if err := ch.QueueBind(queue.Name, binding, t.cfg.Exchange, false, nil); err != nil {
			return fmt.Errorf("amqp: bind %s to %s: %w", queue.Name, binding, err)
		}
	}

	if cfg.PrefetchCount > 0 {
		if err := ch.Qos(cfg.PrefetchCount, 0, false); err != nil {
			return fmt.Errorf("amqp: set prefetch: %w", err)
		}
	}

	deliveries, err := ch.Consume(queue.Name, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("amqp: consume %s: %w", queue.Name, err)
	}

	t.log.Info("amqp: subscribed", "queue", queue.Name, "bindings", cfg.Bindings)

	for {
		select {
		case <-ctx.Done():
			return nil
		case delivery, ok := <-deliveries:
			if !ok {
				return errors.New("amqp: deliveries channel closed")
			}

			t.dispatch(ctx, delivery, handler)
		}
	}
}

// dispatch runs the handler for one delivery and settles it: an acknowledged
// message on success, a rejected one (without requeue) on failure, so a
// poisonous message cannot loop forever.
func (t *Transport) dispatch(ctx context.Context, delivery amqp091.Delivery, handler events.Handler) {
	message := events.Message{
		RoutingKey: delivery.RoutingKey,
		Body:       delivery.Body,
		Headers:    events.Headers(delivery.Headers),
	}

	if err := handler(ctx, message); err != nil {
		t.log.ErrorContext(ctx, "amqp: handler failed, rejecting message",
			"routing_key", delivery.RoutingKey, "error", err)

		if nackErr := delivery.Nack(false, false); nackErr != nil {
			t.log.Warn("amqp: cannot reject message", "error", nackErr)
		}

		return
	}

	if err := delivery.Ack(false); err != nil {
		t.log.Warn("amqp: cannot acknowledge message", "error", err)
	}
}

// channel returns the shared publishing channel, dialing and declaring the
// exchange when the previous connection is missing or closed.
func (t *Transport) channel() (*amqp091.Channel, chan amqp091.Confirmation, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ch != nil && !t.ch.IsClosed() {
		return t.ch, t.confirms, nil
	}

	conn, err := amqp091.Dial(t.cfg.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("amqp: dial: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close()

		return nil, nil, fmt.Errorf("amqp: open channel: %w", err)
	}

	if err := declareExchange(ch, t.cfg.Exchange); err != nil {
		_ = ch.Close()
		_ = conn.Close()

		return nil, nil, err
	}

	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		_ = conn.Close()

		return nil, nil, fmt.Errorf("amqp: enable publisher confirms: %w", err)
	}

	confirms := ch.NotifyPublish(make(chan amqp091.Confirmation, 1))

	t.conn, t.ch, t.confirms = conn, ch, confirms

	return ch, confirms, nil
}

// reset drops the publishing connection so the next publish dials again.
func (t *Transport) reset() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.ch != nil {
		_ = t.ch.Close()
	}

	if t.conn != nil {
		_ = t.conn.Close()
	}

	t.conn, t.ch, t.confirms = nil, nil, nil
}

// declareExchange declares the shared topic exchange as durable.
func declareExchange(ch *amqp091.Channel, exchange string) error {
	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		return fmt.Errorf("amqp: declare exchange %s: %w", exchange, err)
	}

	return nil
}

// queueArguments renders the TTL and dead-letter settings of a queue, the
// delayed delivery mechanism of ADR-0001: a message that sits in the queue
// longer than its TTL is routed to the dead-letter exchange, which is where the
// real consumer listens.
func queueArguments(cfg events.QueueConfig) amqp091.Table {
	args := amqp091.Table{}

	if cfg.MessageTTL > 0 {
		args["x-message-ttl"] = int32(cfg.MessageTTL / time.Millisecond)
	}

	if cfg.DeadLetterExchange != "" {
		args["x-dead-letter-exchange"] = cfg.DeadLetterExchange
	}

	if cfg.DeadLetterRoutingKey != "" {
		args["x-dead-letter-routing-key"] = cfg.DeadLetterRoutingKey
	}

	return args
}
