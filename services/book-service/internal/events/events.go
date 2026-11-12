// Package events defines the book service's domain event contracts.
package events

import (
	"context"
	"time"
)

const (
	TypeBookCreated = "book.created"
	TypeBookUpdated = "book.updated"
	TypeBookDeleted = "book.deleted"
	TypeBookViewed  = "book.viewed"
)

// Publisher delivers a domain event without coupling use cases to a broker.
type Publisher interface {
	Publish(ctx context.Context, eventType string, payload any) error
}

// BookPayload contains the public catalog fields shared by book.created and
// book.updated.
type BookPayload struct {
	TenantID      string `json:"tenant_id"`
	BookID        string `json:"book_id"`
	ISBN          string `json:"isbn"`
	Title         string `json:"title"`
	Author        string `json:"author"`
	Publisher     string `json:"publisher"`
	PublishedYear int    `json:"published_year"`
}

type BookCreated struct {
	BookPayload
}

type BookUpdated struct {
	BookPayload
}

type BookDeleted struct {
	TenantID string `json:"tenant_id"`
	BookID   string `json:"book_id"`
}

type BookViewed struct {
	TenantID   string    `json:"tenant_id"`
	BookID     string    `json:"book_id"`
	OccurredAt time.Time `json:"occurred_at"`
}
