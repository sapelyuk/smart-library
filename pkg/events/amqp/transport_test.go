package amqp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	amqp091 "github.com/rabbitmq/amqp091-go"

	"github.com/sapelyuk/smart-library/pkg/events"
)

func TestQueueArguments(t *testing.T) {
	empty := queueArguments(events.QueueConfig{})
	if len(empty) != 0 {
		t.Errorf("queueArguments(no options) = %v, want empty", empty)
	}

	args := queueArguments(events.QueueConfig{
		MessageTTL:           1500 * time.Millisecond,
		DeadLetterExchange:   "library.events",
		DeadLetterRoutingKey: "loan.reminder",
	})

	if args["x-message-ttl"] != int32(1500) {
		t.Errorf("x-message-ttl = %v, want 1500", args["x-message-ttl"])
	}

	if args["x-dead-letter-exchange"] != "library.events" {
		t.Errorf("x-dead-letter-exchange = %v, want library.events", args["x-dead-letter-exchange"])
	}

	if args["x-dead-letter-routing-key"] != "loan.reminder" {
		t.Errorf("x-dead-letter-routing-key = %v, want loan.reminder", args["x-dead-letter-routing-key"])
	}
}

func TestSubscribeValidatesInput(t *testing.T) {
	transport, err := New(events.Config{URL: "amqp://localhost:5672/", Exchange: "library.events"}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := transport.Subscribe(ctx, events.QueueConfig{}, func(context.Context, events.Message) error { return nil }); err == nil {
		t.Error("Subscribe without a queue name = nil, want an error")
	}

	if err := transport.Subscribe(ctx, events.QueueConfig{Name: "q"}, nil); err == nil {
		t.Error("Subscribe without a handler = nil, want an error")
	}
}

// TestTransportPublishSubscribe exercises the whole path against a real broker:
// an event is published through EventPublisher and must arrive at a subscriber
// with its envelope and headers intact.
//
// It is skipped unless RABBITMQ_TEST_URL points at a broker (the CI test job
// provides one; locally: docker compose up -d rabbitmq).
func TestTransportPublishSubscribe(t *testing.T) {
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL is not set")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	cfg := events.Config{URL: url, Exchange: "library.events.test." + suffix}

	transport, err := New(cfg, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan events.Message, 4)

	go func() {
		_ = transport.Subscribe(ctx, events.QueueConfig{
			Name:          "test.consume." + suffix,
			Bindings:      []string{"test.event.created"},
			PrefetchCount: 1,
		}, func(_ context.Context, msg events.Message) error {
			select {
			case received <- msg:
			default:
			}

			return nil
		})
	}()

	publisher, err := events.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	// The subscriber binds asynchronously, so a message published too early has
	// nowhere to go. Retry until the subscription is up.
	deadline := time.After(20 * time.Second)
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()

	var msg events.Message

wait:
	for {
		if err := publisher.Publish(ctx, "test.event.created", map[string]string{"user_id": "u-1"}); err != nil {
			t.Logf("publish failed, retrying: %v", err)
		}

		select {
		case msg = <-received:
			break wait
		case <-ticker.C:
		case <-deadline:
			t.Fatal("no message received before the deadline")
		}
	}

	event, err := events.DecodeEvent(msg.Body)
	if err != nil {
		t.Fatalf("DecodeEvent: %v", err)
	}

	if event.Type != "test.event.created" {
		t.Errorf("event type = %q, want test.event.created", event.Type)
	}

	if msg.RoutingKey != "test.event.created" {
		t.Errorf("routing key = %q, want test.event.created", msg.RoutingKey)
	}

	if msg.Headers[events.HeaderEventID] != event.ID.String() {
		t.Errorf("event id header = %v, want %s", msg.Headers[events.HeaderEventID], event.ID)
	}
}

// TestTransportDelayedDelivery checks the TTL plus dead-letter mechanism of
// ADR-0001: a message that waits in a queue longer than its TTL is routed to the
// dead-letter exchange, which is where the consumer reads it.
func TestTransportDelayedDelivery(t *testing.T) {
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL is not set")
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	exchange := "library.events.test.delay." + suffix
	waitQueue := "test.wait." + suffix
	consumeQueue := "test.consume." + suffix

	conn, err := amqp091.Dial(url)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("channel: %v", err)
	}

	defer ch.Close()

	if err := ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil); err != nil {
		t.Fatalf("declare exchange: %v", err)
	}

	if _, err := ch.QueueDeclare(waitQueue, true, false, false, false, amqp091.Table{
		"x-message-ttl":             int32(200),
		"x-dead-letter-exchange":    exchange,
		"x-dead-letter-routing-key": "test.event.deliver",
	}); err != nil {
		t.Fatalf("declare waiting queue: %v", err)
	}

	defer func() {
		if _, err := ch.QueueDelete(waitQueue, false, false, false); err != nil {
			t.Logf("cannot delete the waiting queue: %v", err)
		}
	}()

	if err := ch.QueueBind(waitQueue, "test.event.delay", exchange, false, nil); err != nil {
		t.Fatalf("bind waiting queue: %v", err)
	}

	transport, err := New(events.Config{URL: url, Exchange: exchange}, discardLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	defer transport.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	received := make(chan time.Time, 1)

	go func() {
		_ = transport.Subscribe(ctx, events.QueueConfig{
			Name:          consumeQueue,
			Bindings:      []string{"test.event.deliver"},
			PrefetchCount: 1,
		}, func(_ context.Context, _ events.Message) error {
			select {
			case received <- time.Now():
			default:
			}

			return nil
		})
	}()

	publisher, err := events.NewEventPublisher(transport)
	if err != nil {
		t.Fatalf("NewEventPublisher: %v", err)
	}

	time.Sleep(500 * time.Millisecond)

	start := time.Now()
	if err := publisher.Publish(ctx, "test.event.delay", map[string]string{"user_id": "u-2"}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	select {
	case at := <-received:
		if elapsed := at.Sub(start); elapsed < 150*time.Millisecond {
			t.Errorf("message delivered after %s, want at least the TTL of 200ms", elapsed)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("delayed message never arrived")
	}
}

func TestPublishRejectsBadConfiguration(t *testing.T) {
	if _, err := New(events.Config{}, discardLogger()); err == nil {
		t.Error("New(empty config) = nil, want an error")
	}
}

// discardLogger returns a logger that drops its output, keeping the test output
// readable.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
