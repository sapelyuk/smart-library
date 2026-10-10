package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Limits of a query. They are the contract of the service: the REST and gRPC
// surfaces document them, the domain enforces them once, and no adapter has to
// repeat the check.
const (
	// MaxQueryRunes bounds the free-text query. A reader question is a sentence
	// or two; anything longer is either a paste of a document or an attempt to
	// flood the prompt of the backend.
	MaxQueryRunes = 500

	// MaxSessionIDRunes bounds the conversation token the caller may echo back.
	MaxSessionIDRunes = 64

	// DefaultLimit is applied when the request asks for no specific number.
	DefaultLimit = 5

	// MaxLimit caps the recommendations one answer may carry: every extra title
	// costs a retrieval round and reader attention.
	MaxLimit = 20
)

// Query is one validated question of a reader.
//
// Build it with NewQuery: the zero value is not usable, because nothing was
// validated.
type Query struct {
	// Text is the question in free text, trimmed.
	Text string

	// SessionID continues an earlier conversation; empty starts a new one.
	SessionID string

	// Limit is how many titles the answer may carry, always within
	// DefaultLimit..MaxLimit.
	Limit int
}

// NewQuery validates and normalizes a reader question.
//
// A limit of zero means DefaultLimit; a limit above MaxLimit is capped rather
// than rejected — asking for too many titles is not a client error worth
// refusing the whole query for.
func NewQuery(text, sessionID string, limit int) (Query, error) {
	query := Query{
		Text:      strings.TrimSpace(text),
		SessionID: strings.TrimSpace(sessionID),
		Limit:     limit,
	}

	if query.Text == "" {
		return Query{}, ErrEmptyQuery
	}

	if utf8.RuneCountInString(query.Text) > MaxQueryRunes {
		return Query{}, fmt.Errorf("%w: %d characters, maximum is %d",
			ErrQueryTooLong, utf8.RuneCountInString(query.Text), MaxQueryRunes)
	}

	if err := query.validateSessionID(); err != nil {
		return Query{}, err
	}

	switch {
	case query.Limit <= 0:
		query.Limit = DefaultLimit
	case query.Limit > MaxLimit:
		query.Limit = MaxLimit
	}

	return query, nil
}

// validateSessionID keeps the conversation token inside a conservative
// character set.
//
// The token is echoed back into the memory of the backend and stored verbatim,
// so it is treated as untrusted input: spaces and control characters would let
// a caller open a second memory namespace the service cannot address.
func (q Query) validateSessionID() error {
	if q.SessionID == "" {
		return nil
	}

	if utf8.RuneCountInString(q.SessionID) > MaxSessionIDRunes {
		return fmt.Errorf("%w: %d characters, maximum is %d",
			ErrInvalidSessionID, utf8.RuneCountInString(q.SessionID), MaxSessionIDRunes)
	}

	for _, r := range q.SessionID {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			continue
		default:
			return fmt.Errorf("%w: %q is not allowed", ErrInvalidSessionID, r)
		}
	}

	return nil
}

// Source tells the caller how the answer was produced, which is the difference
// between "an assistant phrased this" and "these titles were retrieved".
type Source int

const (
	// SourceUnspecified means the backend did not say.
	SourceUnspecified Source = iota

	// SourceGenerated means the backend produced a natural-language answer.
	SourceGenerated

	// SourceRetrieval means only titles came back: the generation step failed or
	// was skipped, the recommendation itself is still grounded in the catalogue.
	SourceRetrieval
)

// String renders the source for logs and for the domain-level tests.
func (s Source) String() string {
	switch s {
	case SourceGenerated:
		return "generated"
	case SourceRetrieval:
		return "retrieval"
	default:
		return "unspecified"
	}
}

// RecommendedBook is one catalogue entry the pipeline picked. Only the identity
// fields travel: availability and the rest of the metadata belong to
// book-service, which stays the source of truth.
type RecommendedBook struct {
	BookID string
	Title  string
	Author string
}

// Answer is the reply of the RAG backend to a Query.
type Answer struct {
	// Text is the natural-language answer, empty for a retrieval-only reply.
	Text string

	// SessionID is the conversation to echo back for a follow-up question.
	SessionID string

	// Books are the catalogue entries behind the answer, in relevance order.
	Books []RecommendedBook

	// Source says how the answer was produced.
	Source Source
}
