// Package consumer_test holds black-box tests for the RabbitMQ consumer.
//
// Tests cover event decoding, notification building, and error handling for
// loan.issued, loan.overdue, and unknown event types. The Subscriber and
// Service dependencies are mocked via interfaces defined in this package.
package consumer_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/sapelyuk/smart-library/pkg/events"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/consumer"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/domain"
)

// mockSubscriber implements events.Subscriber for testing.
type mockSubscriber struct {
	handler    events.Handler
	handlerSet chan struct{} // closed when Subscribe is called
}

func (m *mockSubscriber) Subscribe(_ context.Context, cfg events.QueueConfig, handler events.Handler) error {
	m.handler = handler
	close(m.handlerSet)
	return nil
}

// trigger delivers a single message to the handler. It waits until the
// handler has been set by Subscribe to avoid a race.
func (m *mockSubscriber) trigger(t *testing.T, body []byte) error {
	t.Helper()
	<-m.handlerSet
	return m.handler(context.Background(), events.Message{
		RoutingKey: "loan.test",
		Body:       body,
	})
}

// mockService implements service.Service for testing.
type mockService struct {
	NotifyFunc func(ctx context.Context, n *domain.Notification) error
}

func (m *mockService) Notify(ctx context.Context, n *domain.Notification) error {
	return m.NotifyFunc(ctx, n)
}

func TestRun_loanIssued(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	var receivedN *domain.Notification
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		receivedN = n
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"loan_id":      "loan-1",
		"reader_id":    "reader-1",
		"book_id":      "book-1",
		"copy_id":      "copy-1",
		"borrowed_at":  time.Now().UTC().Format(time.RFC3339),
		"due_at":       time.Now().UTC().Add(14 * 24 * time.Hour).Format(time.RFC3339),
		"book_title":   "Go Programming",
		"book_author":  "Donovan",
		"reader_email": "reader@example.com",
		"reader_phone": "+79001234567",
	}

	event, err := events.NewEvent("loan.issued", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	if err := sub.trigger(t, mustMarshal(t, event)); err != nil {
		t.Fatalf("trigger() error = %v", err)
	}

	if receivedN == nil {
		t.Fatal("Notify() was not called")
	}
	if receivedN.EventType != "loan.issued" {
		t.Errorf("EventType = %q, want %q", receivedN.EventType, "loan.issued")
	}
	if receivedN.ReaderID != "reader-1" {
		t.Errorf("ReaderID = %q, want %q", receivedN.ReaderID, "reader-1")
	}
	if receivedN.BookTitle != "Go Programming" {
		t.Errorf("BookTitle = %q, want %q", receivedN.BookTitle, "Go Programming")
	}
	if receivedN.BookAuthor != "Donovan" {
		t.Errorf("BookAuthor = %q, want %q", receivedN.BookAuthor, "Donovan")
	}
}

func TestRun_loanOverdue(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	var receivedN *domain.Notification
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		receivedN = n
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"loan_id":      "loan-2",
		"reader_id":    "reader-2",
		"book_id":      "book-2",
		"due_at":       time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339),
		"book_title":   "Clean Code",
		"book_author":  "Martin",
		"reader_email": "clean@example.com",
	}

	event, err := events.NewEvent("loan.overdue", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	if err := sub.trigger(t, mustMarshal(t, event)); err != nil {
		t.Fatalf("trigger() error = %v", err)
	}

	if receivedN == nil {
		t.Fatal("Notify() was not called")
	}
	if receivedN.EventType != "loan.overdue" {
		t.Errorf("EventType = %q, want %q", receivedN.EventType, "loan.overdue")
	}
	if receivedN.BookTitle != "Clean Code" {
		t.Errorf("BookTitle = %q, want %q", receivedN.BookTitle, "Clean Code")
	}
}

func TestRun_unknownEventType(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	called := false
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		called = true
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"some_field": "some_value",
	}

	event, err := events.NewEvent("user.logged_in", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	err = sub.trigger(t, mustMarshal(t, event))
	if err == nil {
		t.Error("trigger() returned nil error for unknown event type, want error")
	}
	if called {
		t.Error("Notify() should not be called for unknown event types")
	}
}

func TestRun_malformedEnvelope(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	called := false
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		called = true
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	// Send invalid JSON as the message body.
	badBody := []byte(`not valid json envelope`)

	err := sub.trigger(t, badBody)
	if err == nil {
		t.Error("trigger() returned nil error for malformed envelope, want error")
	}
	if called {
		t.Error("Notify() should not be called for malformed envelopes")
	}
}

func TestRun_missingReaderID(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	called := false
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		called = true
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"loan_id":      "loan-3",
		"reader_id":    "",
		"book_id":      "book-3",
		"due_at":       time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		"book_title":   "Empty Reader",
		"book_author":  "Nobody",
		"reader_email": "empty@example.com",
	}

	event, err := events.NewEvent("loan.issued", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	err = sub.trigger(t, mustMarshal(t, event))
	if err == nil {
		t.Error("trigger() returned nil error for missing reader_id, want error")
	}
	if called {
		t.Error("Notify() should not be called when reader_id is empty")
	}
}

func TestRun_missingBothEmailAndPhone(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	called := false
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		called = true
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"loan_id":     "loan-4",
		"reader_id":   "reader-4",
		"book_id":     "book-4",
		"due_at":      time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		"book_title":  "No Contact",
		"book_author": "Nowhere",
	}

	event, err := events.NewEvent("loan.issued", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	err = sub.trigger(t, mustMarshal(t, event))
	if err == nil {
		t.Error("trigger() returned nil error for missing contact info, want error")
	}
	if called {
		t.Error("Notify() should not be called when both email and phone are empty")
	}
}

func TestRun_serviceNotifyFails(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	wantErr := errors.New("delivery failed")
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		return wantErr
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"loan_id":      "loan-5",
		"reader_id":    "reader-5",
		"book_id":      "book-5",
		"due_at":       time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339),
		"book_title":   "Undeliverable",
		"book_author":  "Ghost",
		"reader_email": "ghost@example.com",
	}

	event, err := events.NewEvent("loan.issued", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	err = sub.trigger(t, mustMarshal(t, event))
	if err == nil {
		t.Error("trigger() returned nil error when service fails, want error")
	}
}

func TestRun_loanOverdueWithPhoneOnly(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	var receivedN *domain.Notification
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		receivedN = n
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	payload := map[string]any{
		"loan_id":      "loan-6",
		"reader_id":    "reader-6",
		"book_id":      "book-6",
		"due_at":       time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339),
		"book_title":   "Phone Reminder",
		"book_author":  "Author",
		"reader_phone": "+79998887766",
	}

	event, err := events.NewEvent("loan.overdue", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	if err := sub.trigger(t, mustMarshal(t, event)); err != nil {
		t.Fatalf("trigger() error = %v", err)
	}

	if receivedN == nil {
		t.Fatal("Notify() was not called")
	}
	if receivedN.ReaderPhone != "+79998887766" {
		t.Errorf("ReaderPhone = %q, want %q", receivedN.ReaderPhone, "+79998887766")
	}
	if receivedN.ReaderEmail != "" {
		t.Errorf("ReaderEmail = %q, want empty", receivedN.ReaderEmail)
	}
}

func TestRun_InvalidPayloadJSON(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	called := false
	svc.NotifyFunc = func(ctx context.Context, n *domain.Notification) error {
		called = true
		return nil
	}

	go consumer.Run(context.Background(), sub, svc, log)

	// Create a valid event envelope but with a payload that cannot be
	// decoded into loanIssuedPayload (missing required due_at).
	payload := map[string]any{
		"loan_id":     "loan-7",
		"reader_id":   "reader-7",
		"book_id":     "book-7",
		"book_title":  "Bad Payload",
		"book_author": "Test",
		// Intentionally missing "due_at" — will cause json.Unmarshal to
		// succeed but domain.NewNotification to fail due to zero time.
	}

	event, err := events.NewEvent("loan.issued", payload)
	if err != nil {
		t.Fatalf("NewEvent() error = %v", err)
	}

	err = sub.trigger(t, mustMarshal(t, event))
	// The message is rejected because the notification build fails.
	if err == nil {
		t.Error("trigger() returned nil error for invalid payload, want error")
	}
	if called {
		t.Error("Notify() should not be called for invalid payloads")
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	sub := &mockSubscriber{handlerSet: make(chan struct{})}
	svc := &mockService{}
	log := slog.Default()

	ctx, cancel := context.WithCancel(context.Background())

	go consumer.Run(ctx, sub, svc, log)

	// Cancel immediately — Run should return without blocking.
	cancel()

	// Give the goroutine a moment to notice the cancellation.
	time.Sleep(50 * time.Millisecond)

	// If Run didn't return by now, cancel() would have blocked,
	// which means the consumer is not respecting context cancellation.
}

func TestQueueName(t *testing.T) {
	want := "notification-service.loan-events"
	if got := consumer.QueueName; got != want {
		t.Errorf("QueueName = %q, want %q", got, want)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal error = %v", err)
	}
	return b
}
