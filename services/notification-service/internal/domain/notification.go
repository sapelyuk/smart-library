// Package domain holds the domain types of the notification service.
//
// The service has no database of its own; it consumes loan.* events from
// RabbitMQ and sends email/SMS reminders. The domain layer models the data
// that flows through the consumer and the delivery logic.
package domain

import (
	"fmt"
	"time"
)

// Notification represents a reminder to be delivered to a reader.
type Notification struct {
	ID        string
	ReaderID  string
	ReaderEmail string
	ReaderPhone string
	BookTitle string
	BookAuthor string
	DueAt     time.Time
	EventType string // loan.issued | loan.overdue
	CreatedAt time.Time
}

// NewNotification builds a reminder from a loan event payload.
func NewNotification(readerID, readerEmail, readerPhone, bookTitle, bookAuthor string, dueAt time.Time, eventType string) (*Notification, error) {
	if readerID == "" {
		return nil, fmt.Errorf("notification: reader_id is required")
	}

	if readerEmail == "" && readerPhone == "" {
		return nil, fmt.Errorf("notification: at least one of reader_email or reader_phone must be set")
	}

	return &Notification{
		ReaderID:    readerID,
		ReaderEmail: readerEmail,
		ReaderPhone: readerPhone,
		BookTitle:   bookTitle,
		BookAuthor:  bookAuthor,
		DueAt:       dueAt.UTC(),
		EventType:   eventType,
		CreatedAt:   time.Now().UTC(),
	}, nil
}

// Subject returns the email subject for this notification.
func (n *Notification) Subject() string {
	switch n.EventType {
	case "loan.issued":
		return fmt.Sprintf("Книга выдана: %s", n.BookTitle)
	case "loan.overdue":
	 return fmt.Sprintf("Напоминание: срок возврата %s", n.BookTitle)
	default:
		return fmt.Sprintf("Уведомление: %s", n.BookTitle)
	}
}

// Body returns the email body for this notification.
func (n *Notification) Body() string {
	switch n.EventType {
	case "loan.issued":
		return fmt.Sprintf(
			"Здравствуйте!\n\n"+
				"Вам выдана книга: %s (%s).\n\n"+
				"Срок возврата: %s.\n\n"+
				"Спасибо, что пользуетесь нашей библиотекой!",
			n.BookTitle,
			n.BookAuthor,
			n.DueAt.Format("02.01.2006"),
		)
	case "loan.overdue":
		return fmt.Sprintf(
			"Здравствуйте!\n\n"+
				"Напоминаем, что срок возврата книги: %s (%s).\n\n"+
				"Срок истёк: %s.\n\n"+
				"Пожалуйста, верните книгу как можно скорее.",
			n.BookTitle,
			n.BookAuthor,
			n.DueAt.Format("02.01.2006"),
		)
	default:
		return fmt.Sprintf("Уведомление: %s", n.BookTitle)
	}
}
