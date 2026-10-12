package domain

import (
	"testing"
	"time"
)

func TestNewNotification(t *testing.T) {
	now := time.Now().UTC()

	tests := []struct {
		name         string
		readerID     string
		readerEmail  string
		readerPhone  string
		bookTitle    string
		bookAuthor   string
		dueAt        time.Time
		eventType    string
		wantErr      bool
		wantSubject  string
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
			wantErr:     false,
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
		})
	}
}

func TestNotification_Subject(t *testing.T) {
	now := time.Now().UTC()
	n, _ := NewNotification("r1", "a@b.com", "", "Test Book", "Author", now, "loan.issued")

	if got := n.Subject(); got != "Книга выдана: Test Book" {
		t.Errorf("Subject() = %q", got)
	}
}

func TestNotification_Body(t *testing.T) {
	now := time.Now().UTC()
	n, _ := NewNotification("r1", "a@b.com", "", "Test Book", "Author", now, "loan.issued")

	body := n.Body()
	if len(body) == 0 {
		t.Error("Body() is empty")
	}
}
