package domain

import (
	"testing"
	"time"
)

func TestNewNotification(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name        string
		readerID    string
		readerEmail string
		readerPhone string
		bookTitle   string
		bookAuthor  string
		dueAt       time.Time
		eventType   string
		wantErr     bool
		wantSubject string
	}{
		{
			name:        "valid issued notification",
			readerID:    "reader-1",
			readerEmail: "reader@example.com",
			bookTitle:   "Go Programming",
			bookAuthor:  "Donovan",
			dueAt:       now.Add(14 * 24 * time.Hour),
			eventType:   "loan.issued",
			wantErr:     false,
			wantSubject: "Книга выдана: Go Programming",
		},
		{
			name:        "valid overdue notification",
			readerID:    "reader-1",
			readerEmail: "reader@example.com",
			bookTitle:   "Go Programming",
			bookAuthor:  "Donovan",
			dueAt:       now.Add(-24 * time.Hour),
			eventType:   "loan.overdue",
			wantErr:     false,
			wantSubject: "Напоминание: срок возврата Go Programming",
		},
		{
			name:      "missing reader ID",
			readerID:  "",
			bookTitle: "Go Programming",
			wantErr:   true,
		},
		{
			name:        "missing both email and phone",
			readerID:    "reader-1",
			bookTitle:   "Go Programming",
			readerEmail: "",
			readerPhone: "",
			wantErr:     true,
		},
		{
			name:        "phone only (no email)",
			readerID:    "reader-1",
			readerPhone: "+79001234567",
			bookTitle:   "Go Programming",
			dueAt:       now.Add(7 * 24 * time.Hour),
			wantErr:     false,
		},
		{
			name:        "both email and phone set",
			readerID:    "reader-1",
			readerEmail: "reader@example.com",
			readerPhone: "+79001234567",
			bookTitle:   "Go Programming",
			dueAt:       now.Add(7 * 24 * time.Hour),
			wantErr:     false,
		},
		{
			name:        "empty book title",
			readerID:    "reader-1",
			readerEmail: "reader@example.com",
			bookTitle:   "",
			dueAt:       now.Add(7 * 24 * time.Hour),
			wantErr:     false,
		},
		{
			name:        "default event type",
			readerID:    "reader-1",
			readerEmail: "reader@example.com",
			bookTitle:   "Test Book",
			eventType:   "unknown.event",
			dueAt:       now.Add(7 * 24 * time.Hour),
			wantErr:     false,
			wantSubject: "Уведомление: Test Book",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := NewNotification(
				tt.readerID,
				tt.readerEmail,
				tt.readerPhone,
				tt.bookTitle,
				tt.bookAuthor,
				tt.dueAt,
				tt.eventType,
			)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if tt.wantSubject != "" && n.Subject() != tt.wantSubject {
				t.Errorf("Subject() = %q, want %q", n.Subject(), tt.wantSubject)
			}
			if n.ReaderID != tt.readerID {
				t.Errorf("ReaderID = %q, want %q", n.ReaderID, tt.readerID)
			}
			if n.DueAt == (time.Time{}) {
				t.Error("DueAt is zero value")
			}
			if n.CreatedAt.IsZero() {
				t.Error("CreatedAt is zero value")
			}
		})
	}
}

func TestNotification_Subject(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		eventType string
		bookTitle string
		want      string
	}{
		{
			eventType: "loan.issued",
			bookTitle: "Go Programming",
			want:      "Книга выдана: Go Programming",
		},
		{
			eventType: "loan.overdue",
			bookTitle: "Clean Code",
			want:      "Напоминание: срок возврата Clean Code",
		},
		{
			eventType: "unknown.event",
			bookTitle: "Mystery Book",
			want:      "Уведомление: Mystery Book",
		},
		{
			eventType: "loan.issued",
			bookTitle: "",
			want:      "Книга выдана: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			n, err := NewNotification("r1", "a@b.com", "", tt.bookTitle, "Author", now, tt.eventType)
			if err != nil {
				t.Fatalf("NewNotification() error = %v", err)
			}
			if got := n.Subject(); got != tt.want {
				t.Errorf("Subject() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNotification_Body(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		eventType  string
		bookTitle  string
		bookAuthor string
		wantSub    []string
	}{
		{
			eventType:  "loan.issued",
			bookTitle:  "Go Programming",
			bookAuthor: "Donovan",
			wantSub:    []string{"Вам выдана книга", "Go Programming", "Donovan", "Срок возврата"},
		},
		{
			eventType:  "loan.overdue",
			bookTitle:  "Clean Code",
			bookAuthor: "Martin",
			wantSub:    []string{"Напоминаем", "Clean Code", "Martin", "Срок истёк"},
		},
		{
			eventType: "unknown",
			bookTitle: "Mystery",
			wantSub:   []string{"Уведомление: Mystery"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			n, err := NewNotification("r1", "a@b.com", "", tt.bookTitle, tt.bookAuthor, now, tt.eventType)
			if err != nil {
				t.Fatalf("NewNotification() error = %v", err)
			}

			body := n.Body()
			if len(body) == 0 {
				t.Error("Body() is empty")
			}

			for _, want := range tt.wantSub {
				if !contains(body, want) {
					t.Errorf("Body() = %q, expected substring %q", body, want)
				}
			}
		})
	}
}

func TestNotification_DueAtIsUTC(t *testing.T) {
	// Create a notification with a non-UTC time.
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("cannot load location: %v", err)
	}
	nonUTC := time.Date(2026, 6, 15, 12, 0, 0, 0, loc)

	n, err := NewNotification("r1", "a@b.com", "", "Book", "Author", nonUTC, "loan.issued")
	if err != nil {
		t.Fatalf("NewNotification() error = %v", err)
	}

	if n.DueAt.Location().String() != "UTC" {
		t.Errorf("DueAt location = %v, want UTC", n.DueAt.Location())
	}
}

func TestNotification_CreatedAtIsUTC(t *testing.T) {
	n, err := NewNotification("r1", "a@b.com", "", "Book", "Author", time.Now(), "loan.issued")
	if err != nil {
		t.Fatalf("NewNotification() error = %v", err)
	}

	if n.CreatedAt.Location().String() != "UTC" {
		t.Errorf("CreatedAt location = %v, want UTC", n.CreatedAt.Location())
	}
}

func TestErrInvalidRecipient(t *testing.T) {
	if ErrInvalidRecipient == nil {
		t.Error("ErrInvalidRecipient is nil, want non-nil")
	}
}

func TestErrDeliveryFailed(t *testing.T) {
	if ErrDeliveryFailed == nil {
		t.Error("ErrDeliveryFailed is nil, want non-nil")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && search(s, substr)
}

func search(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
