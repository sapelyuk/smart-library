package domain

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// MaxIngestTextRunes bounds the text the indexing pipeline receives for one
// book. The catalogue of this platform carries bibliographic records rather than
// full texts, so a few thousand characters is generous; the cap protects the
// embedding quota from a record that carries a whole novel in one field.
const MaxIngestTextRunes = 8000

// CatalogBook is the slice of the catalogue this service indexes. It is filled
// from book-service, which owns the catalog, and it is deliberately a subset:
// the index does not need copy counts, and copying them would only invite the
// index to disagree with the inventory.
type CatalogBook struct {
	// ID is the catalog identifier, a UUID assigned by book-service. It is the
	// idempotency key of the index: reindexing the same ID replaces the previous
	// chunks instead of adding a second copy.
	ID string

	ISBN          string
	Title         string
	Author        string
	Publisher     string
	PublishedYear int

	// Description is the blurb of the edition, empty when the catalogue has
	// none. It is the most valuable retrieval text the platform has.
	Description string

	// Content is the text to chunk and embed, empty when the catalogue carries
	// no text. IngestText falls back to the bibliographic record.
	Content string
}

// Validate reports whether the record can be indexed. An identifier and a title
// are the minimum: without them the entry cannot be pointed at later.
func (b CatalogBook) Validate() error {
	if _, err := ParseBookID(b.ID); err != nil {
		return err
	}

	if strings.TrimSpace(b.Title) == "" {
		return fmt.Errorf("%w: the book has no title to index", ErrInvalidBookID)
	}

	return nil
}

// IngestText renders the text handed to the indexing pipeline.
//
// When the catalogue carries a text, that text is indexed as is. Otherwise the
// bibliographic record is rendered into a short prose line, because an empty
// document would produce no chunks and the book would silently disappear from
// retrieval. The result is capped at MaxIngestTextRunes.
func (b CatalogBook) IngestText() string {
	if text := strings.TrimSpace(b.Content); text != "" {
		return truncate(text, MaxIngestTextRunes)
	}

	var parts []string

	if author := strings.TrimSpace(b.Author); author != "" {
		parts = append(parts, author)
	}

	if year := b.PublishedYear; year > 0 {
		parts = append(parts, strconv.Itoa(year))
	}

	if publisher := strings.TrimSpace(b.Publisher); publisher != "" {
		parts = append(parts, publisher)
	}

	if isbn := strings.TrimSpace(b.ISBN); isbn != "" {
		parts = append(parts, "ISBN "+isbn)
	}

	line := strings.TrimSpace(b.Title)
	if len(parts) > 0 {
		line += " (" + strings.Join(parts, ", ") + ")"
	}

	if description := strings.TrimSpace(b.Description); description != "" {
		line += ". " + description
	}

	return truncate(line, MaxIngestTextRunes)
}

// ParseBookID normalizes a book identifier coming from a request or from an
// event.
//
// The catalog hands out UUIDs, so anything else is rejected rather than indexed
// under a key the catalog can never produce: an index entry that no query can
// resolve back to a book is dead weight that retrieval still has to skip.
func ParseBookID(raw string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("%w: %q", ErrInvalidBookID, strings.TrimSpace(raw))
	}

	return parsed.String(), nil
}

// IndexDeletion reports what dropping one book from the vector store removed.
type IndexDeletion struct {
	BookID string

	// Chunks is the number of index rows that went away with the book.
	Chunks int

	// Deleted is false when the book was not indexed at all.
	Deleted bool
}

// IndexStats is the size of the vector store, used by the status endpoint and by
// the smoke checks of a deployment.
type IndexStats struct {
	Books  int
	Chunks int
}

// truncate cuts s to at most max runes, on a rune boundary.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}

	runes := []rune(s)
	if len(runes) <= max {
		return s
	}

	return strings.TrimRight(string(runes[:max]), " \t\n")
}
