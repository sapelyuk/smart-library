package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestNewQuery_Empty checks that an empty or whitespace-only query is rejected
// with ErrEmptyQuery.
func TestNewQuery_Empty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t "} {
		_, err := NewQuery(raw, "", 0)
		if !errors.Is(err, ErrEmptyQuery) {
			t.Fatalf("NewQuery(%q) error = %v, want ErrEmptyQuery", raw, err)
		}
	}
}

// TestNewQuery_Trim checks that leading and trailing spaces are trimmed from the text.
func TestNewQuery_Trim(t *testing.T) {
	q, err := NewQuery("  Где книга про DDD?  ", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.Text != "Где книга про DDD?" {
		t.Fatalf("Text = %q, want %q", q.Text, "Где книга про DDD?")
	}
}

// TestNewQuery_TooLong checks that a query longer than MaxQueryRunes is rejected
// with ErrQueryTooLong.
func TestNewQuery_TooLong(t *testing.T) {
	long := strings.Repeat("a", MaxQueryRunes+1)

	_, err := NewQuery(long, "", 0)
	if !errors.Is(err, ErrQueryTooLong) {
		t.Fatalf("NewQuery() error = %v, want ErrQueryTooLong", err)
	}
}

// TestNewQuery_LimitBounds checks that limit=0 is replaced with DefaultLimit
// and limit>MaxLimit is clamped to MaxLimit.
func TestNewQuery_LimitBounds(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"zero replaced with default", 0, DefaultLimit},
		{"negative replaced with default", -5, DefaultLimit},
		{"in range kept as is", 7, 7},
		{"above cap clamped to cap", MaxLimit + 1, MaxLimit},
		{"exactly cap accepted", MaxLimit, MaxLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := NewQuery("valid question here", "", tt.limit)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if q.Limit != tt.want {
				t.Fatalf("Limit = %d, want %d", q.Limit, tt.want)
			}
		})
	}
}

// TestNewQuery_SessionID checks that valid characters in a session id are
// accepted and invalid ones are rejected with ErrInvalidSessionID.
func TestNewQuery_SessionID(t *testing.T) {
	valid := []string{"", "abc-123_X.y", "A"}
	for _, sid := range valid {
		if _, err := NewQuery("valid question", sid, 0); err != nil {
			t.Fatalf("session id %q rejected: %v", sid, err)
		}
	}

	invalid := []string{"has space", "has/slash", strings.Repeat("x", MaxSessionIDRunes+1)}
	for _, sid := range invalid {
		_, err := NewQuery("valid question", sid, 0)
		if !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("session id %q accepted, want ErrInvalidSessionID (got %v)", sid, err)
		}
	}
}

// TestSource_String checks the human-readable representation of the answer source.
func TestSource_String(t *testing.T) {
	tests := []struct {
		src  Source
		want string
	}{
		{SourceUnspecified, "unspecified"},
		{SourceGenerated, "generated"},
		{SourceRetrieval, "retrieval"},
		{Source(99), "unspecified"},
	}

	for _, tt := range tests {
		if got := tt.src.String(); got != tt.want {
			t.Fatalf("Source(%d).String() = %q, want %q", tt.src, got, tt.want)
		}
	}
}

// TestParseBookID checks that a valid UUID is returned in canonical form and
// an invalid one is rejected with ErrInvalidBookID.
func TestParseBookID(t *testing.T) {
	const id = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	got, err := ParseBookID("  " + id + "  ")
	if err != nil {
		t.Fatalf("ParseBookID() error: %v", err)
	}

	if got != id {
		t.Fatalf("ParseBookID() = %q, want %q", got, id)
	}

	for _, raw := range []string{"", "not-a-uuid", "123"} {
		_, err := ParseBookID(raw)
		if !errors.Is(err, ErrInvalidBookID) {
			t.Fatalf("ParseBookID(%q) error = %v, want ErrInvalidBookID", raw, err)
		}
	}
}

// TestCatalogBook_Validate checks that a book without an ID or without a title
// is not indexed.
func TestCatalogBook_Validate(t *testing.T) {
	valid := CatalogBook{
		ID:    "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		Title: "Clean Architecture",
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("valid book rejected: %v", err)
	}

	noID := CatalogBook{ID: "not-uuid", Title: "X"}
	if err := noID.Validate(); !errors.Is(err, ErrInvalidBookID) {
		t.Fatalf("book with invalid ID: error = %v, want ErrInvalidBookID", err)
	}

	noTitle := CatalogBook{ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "   "}
	if err := noTitle.Validate(); !errors.Is(err, ErrInvalidBookID) {
		t.Fatalf("book without title: error = %v, want ErrInvalidBookID", err)
	}
}

// TestCatalogBook_IngestText checks that when Content is empty a line is built
// from bibliographic fields, and when non-empty Content is used as is.
func TestCatalogBook_IngestText(t *testing.T) {
	t.Run("content takes priority", func(t *testing.T) {
		b := CatalogBook{
			ID:      "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:   "Title",
			Content: "  full book text  ",
		}

		if got := b.IngestText(); got != "full book text" {
			t.Fatalf("IngestText() = %q, want %q", got, "full book text")
		}
	})

	t.Run("falls back to bibliography without description", func(t *testing.T) {
		b := CatalogBook{
			ID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:         "Clean Architecture",
			Author:        "Robert C. Martin",
			PublishedYear: 2017,
			Publisher:     "Prentice Hall",
			ISBN:          "978-0134494166",
		}

		got := b.IngestText()

		for _, want := range []string{"Clean Architecture", "Robert C. Martin", "2017", "Prentice Hall", "978-0134494166"} {
			if !strings.Contains(got, want) {
				t.Fatalf("IngestText() = %q, does not contain %q", got, want)
			}
		}
	})

	t.Run("description is appended to the line", func(t *testing.T) {
		b := CatalogBook{
			ID:          "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:       "Domain-Driven Design",
			Description: "Стратегическое проектирование",
		}

		got := b.IngestText()
		if !strings.Contains(got, "Стратегическое проектирование") {
			t.Fatalf("IngestText() = %q, does not contain description", got)
		}
	})

	t.Run("truncated to MaxIngestTextRunes", func(t *testing.T) {
		b := CatalogBook{
			ID:      "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:   "X",
			Content: strings.Repeat("a", MaxIngestTextRunes+100),
		}

		if got := len(b.IngestText()); got > MaxIngestTextRunes {
			t.Fatalf("IngestText() length %d, max %d", got, MaxIngestTextRunes)
		}
	})
}

// TestTruncate_RuneBoundary checks that truncate cuts on rune boundaries,
// not bytes.
func TestTruncate_RuneBoundary(t *testing.T) {
	// Cyrillic: 2 bytes per rune.
	src := "Привет мир"

	// max=6 runes — first 6 runes of src.
	got := truncate(src, 6)
	if runeLen := len([]rune(got)); runeLen > 6 {
		t.Fatalf("truncate() returned %d runes, want ≤6: %q", runeLen, got)
	}

	// Trailing spaces are trimmed.
	got = truncate("привет     ", 7)
	if got != "привет" {
		t.Fatalf("truncate() = %q, want %q", got, "привет")
	}
}

// TestTruncate_ShortString checks that a short string is returned as is.
func TestTruncate_ShortString(t *testing.T) {
	src := "short"
	if got := truncate(src, 100); got != src {
		t.Fatalf("truncate() = %q, want %q", got, src)
	}
}
