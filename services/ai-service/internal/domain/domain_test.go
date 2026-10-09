package domain

import (
	"errors"
	"strings"
	"testing"
)

// TestNewQuery_PustoyVoprosпроверяет, что пустой или пробельный вопрос
// отклоняется с ErrEmptyQuery.
func TestNewQuery_Empty(t *testing.T) {
	for _, raw := range []string{"", "   ", "\n\t "} {
		_, err := NewQuery(raw, "", 0)
		if !errors.Is(err, ErrEmptyQuery) {
			t.Fatalf("NewQuery(%q) error = %v, хотим ErrEmptyQuery", raw, err)
		}
	}
}

// TestNewQuery_Trim проверяет, что пробелы по краям текста срезаются.
func TestNewQuery_Trim(t *testing.T) {
	q, err := NewQuery("  Где книга про DDD?  ", "", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if q.Text != "Где книга про DDD?" {
		t.Fatalf("Text = %q, хотим %q", q.Text, "Где книга про DDD?")
	}
}

// TestNewQuery_TooLong проверяет, что вопрос длиннее MaxQueryRunes
// отклоняется с ErrQueryTooLong.
func TestNewQuery_TooLong(t *testing.T) {
	long := strings.Repeat("a", MaxQueryRunes+1)

	_, err := NewQuery(long, "", 0)
	if !errors.Is(err, ErrQueryTooLong) {
		t.Fatalf("NewQuery() error = %v, хотим ErrQueryTooLong", err)
	}
}

// TestNewQuery_LimitBounds проверяет, что limit=0 заменяется DefaultLimit,
// а limit>MaxLimit ограничивается MaxLimit.
func TestNewQuery_LimitBounds(t *testing.T) {
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{"ноль заменяется default", 0, DefaultLimit},
		{"отрицательный заменяется default", -5, DefaultLimit},
		{"в пределах остаётся как есть", 7, 7},
		{"выше cap ограничивается cap", MaxLimit + 1, MaxLimit},
		{"ровно cap принимается", MaxLimit, MaxLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q, err := NewQuery("valid question here", "", tt.limit)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if q.Limit != tt.want {
				t.Fatalf("Limit = %d, хотим %d", q.Limit, tt.want)
			}
		})
	}
}

// TestNewQuery_SessionIDValid проверяет, что допустимые символы в session id
// принимаются, а недопустимые отклоняются с ErrInvalidSessionID.
func TestNewQuery_SessionID(t *testing.T) {
	valid := []string{"", "abc-123_X.y", "A"}
	for _, sid := range valid {
		if _, err := NewQuery("valid question", sid, 0); err != nil {
			t.Fatalf("session id %q отклонён: %v", sid, err)
		}
	}

	invalid := []string{"has space", "has/slash", strings.Repeat("x", MaxSessionIDRunes+1)}
	for _, sid := range invalid {
		_, err := NewQuery("valid question", sid, 0)
		if !errors.Is(err, ErrInvalidSessionID) {
			t.Fatalf("session id %q принят, хотим ErrInvalidSessionID (получили %v)", sid, err)
		}
	}
}

// TestSource_String проверяет читаемое представление источника ответа.
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
			t.Fatalf("Source(%d).String() = %q, хотим %q", tt.src, got, tt.want)
		}
	}
}

// TestParseBookID_ValidUUID проверяет, что корректный UUID возвращается
// в канонической форме, а некорректный — с ErrInvalidBookID.
func TestParseBookID(t *testing.T) {
	const id = "6ba7b810-9dad-11d1-80b4-00c04fd430c8"

	got, err := ParseBookID("  " + id + "  ")
	if err != nil {
		t.Fatalf("ParseBookID() error: %v", err)
	}

	if got != id {
		t.Fatalf("ParseBookID() = %q, хотим %q", got, id)
	}

	for _, raw := range []string{"", "not-a-uuid", "123"} {
		_, err := ParseBookID(raw)
		if !errors.Is(err, ErrInvalidBookID) {
			t.Fatalf("ParseBookID(%q) error = %v, хотим ErrInvalidBookID", raw, err)
		}
	}
}

// TestCatalogBook_Validate проверяет, что книга без ID или без названия
// не индексируется.
func TestCatalogBook_Validate(t *testing.T) {
	valid := CatalogBook{
		ID:    "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		Title: "Clean Architecture",
	}

	if err := valid.Validate(); err != nil {
		t.Fatalf("корректная книга отклонена: %v", err)
	}

	noID := CatalogBook{ID: "not-uuid", Title: "X"}
	if err := noID.Validate(); !errors.Is(err, ErrInvalidBookID) {
		t.Fatalf("книга с некорректным ID: error = %v, хотим ErrInvalidBookID", err)
	}

	noTitle := CatalogBook{ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "   "}
	if err := noTitle.Validate(); !errors.Is(err, ErrInvalidBookID) {
		t.Fatalf("книга без названия: error = %v, хотим ErrInvalidBookID", err)
	}
}

// TestCatalogBook_IngestText проверяет, что при пустом Content формируется
// строка из библиографических полей, а при непустом — используется Content.
func TestCatalogBook_IngestText(t *testing.T) {
	t.Run("content имеет приоритет", func(t *testing.T) {
		b := CatalogBook{
			ID:      "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:   "Title",
			Content: "  полный текст книги  ",
		}

		if got := b.IngestText(); got != "полный текст книги" {
			t.Fatalf("IngestText() = %q, хотим %q", got, "полный текст книги")
		}
	})

	t.Run("fallback на библиографию без description", func(t *testing.T) {
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
				t.Fatalf("IngestText() = %q, не содержит %q", got, want)
			}
		}
	})

	t.Run("description добавляется к строке", func(t *testing.T) {
		b := CatalogBook{
			ID:          "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:       "Domain-Driven Design",
			Description: "Стратегическое проектирование",
		}

		got := b.IngestText()
		if !strings.Contains(got, "Стратегическое проектирование") {
			t.Fatalf("IngestText() = %q, не содержит description", got)
		}
	})

	t.Run("обрезается до MaxIngestTextRunes", func(t *testing.T) {
		b := CatalogBook{
			ID:      "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
			Title:   "X",
			Content: strings.Repeat("a", MaxIngestTextRunes+100),
		}

		if got := len(b.IngestText()); got > MaxIngestTextRunes {
			t.Fatalf("IngestText() длина %d, максимум %d", got, MaxIngestTextRunes)
		}
	})
}

// TestTruncate_RuneBoundary проверяет, что truncate режет по границе рун,
// а не по байтам.
func TestTruncate_RuneBoundary(t *testing.T) {
	// Кириллица: 2 байта на руну.
	src := "Привет мир"

	// max=6 рун — "Приве" без "т".
	got := truncate(src, 6)
	if runeLen := len([]rune(got)); runeLen > 6 {
		t.Fatalf("truncate() вернул %d рун, хотим ≤6: %q", runeLen, got)
	}

	// Хвостовые пробелы срезаются.
	got = truncate("привет     ", 7)
	if got != "привет" {
		t.Fatalf("truncate() = %q, хотим %q", got, "привет")
	}
}

// TestTruncate_ShortString проверяет, что короткая строка возвращается как есть.
func TestTruncate_ShortString(t *testing.T) {
	src := "short"
	if got := truncate(src, 100); got != src {
		t.Fatalf("truncate() = %q, хотим %q", got, src)
	}
}
