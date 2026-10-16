package n8n_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/n8n"
)

// --- New ---

func TestNewRejectsInvalidConfig(t *testing.T) {
	for _, tt := range []struct {
		name string
		cfg  n8n.Config
	}{
		{"empty base url", n8n.Config{BaseURL: "", HeaderName: "X-Key", HeaderValue: "v"}},
		{"bad url", n8n.Config{BaseURL: "://bad", HeaderName: "X-Key", HeaderValue: "v"}},
		{"non-http scheme", n8n.Config{BaseURL: "ftp://host", HeaderName: "X-Key", HeaderValue: "v"}},
		{"missing header name", n8n.Config{BaseURL: "http://host", HeaderName: "", HeaderValue: "v"}},
		{"missing header value", n8n.Config{BaseURL: "http://host", HeaderName: "X-Key", HeaderValue: ""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := n8n.New(tt.cfg); err == nil {
				t.Error("expected error for invalid config")
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := n8n.New(n8n.Config{
		BaseURL:     "http://localhost:5678",
		HeaderName:  "X-Auth",
		HeaderValue: "secret",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}

func TestNewCustomPaths(t *testing.T) {
	c, err := n8n.New(n8n.Config{
		BaseURL:       "http://localhost:5678",
		HeaderName:    "X-Auth",
		HeaderValue:   "secret",
		RecommendPath: "/custom/recommend",
		IngestPath:    "/custom/ingest",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c == nil {
		t.Fatal("client is nil")
	}
}
// --- Recommend ---

func TestRecommendSuccess(t *testing.T) {
	var gotBody map[string]any
	var gotHeader http.Header
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"output":"  Try DDD by Evans  ","sessionId":"s-1"}`))
	}))
	defer srv.Close()

	c, err := n8n.New(n8n.Config{
		BaseURL:     srv.URL,
		HeaderName:  "X-N8n-Auth",
		HeaderValue: "test-secret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	answer, err := c.Recommend(context.Background(), domain.Query{
		Text:      "books on DDD",
		SessionID: "s-1",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if answer.Text != "Try DDD by Evans" {
		t.Errorf("Text = %q, want trimmed", answer.Text)
	}
	if answer.SessionID != "s-1" {
		t.Errorf("SessionID = %q, want s-1", answer.SessionID)
	}
	if answer.Source != domain.SourceGenerated {
		t.Errorf("Source = %v, want SourceGenerated", answer.Source)
	}

	// Verify request shape.
	if gotPath != n8n.DefaultRecommendPath {
		t.Errorf("path = %q, want %q", gotPath, n8n.DefaultRecommendPath)
	}
	if gotHeader.Get("X-N8n-Auth") != "test-secret" {
		t.Errorf("auth header = %q, want test-secret", gotHeader.Get("X-N8n-Auth"))
	}
	if gotHeader.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotHeader.Get("Content-Type"))
	}
	if gotBody["chatInput"] != "books on DDD" {
		t.Errorf("chatInput = %v, want 'books on DDD'", gotBody["chatInput"])
	}
	if gotBody["sessionId"] != "s-1" {
		t.Errorf("sessionId = %v, want s-1", gotBody["sessionId"])
	}
}

func TestRecommendNoSessionID(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"output":"answer"}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	_, err := c.Recommend(context.Background(), domain.Query{Text: "q", SessionID: ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// sessionId must not be present in the body when the query has none.
	if _, present := gotBody["sessionId"]; present {
		t.Error("sessionId should not be sent when the query has none")
	}
}

func TestRecommendEmptyOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"output":"   ","sessionId":"s"}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	_, err := c.Recommend(context.Background(), domain.Query{Text: "q"})
	if !errors.Is(err, domain.ErrBackendUnavailable) {
		t.Errorf("error = %v, want ErrBackendUnavailable", err)
	}
}

func TestRecommendNon2xx(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404, 429, 500, 502} {
		t.Run("", func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(code)
				w.Write([]byte("error"))
			}))
			defer srv.Close()

			c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

			_, err := c.Recommend(context.Background(), domain.Query{Text: "q"})
			if err == nil {
				t.Fatal("expected error for non-2xx")
			}

			// 4xx (except 401/403/404/429) should be BackendRejected; the rest BackendUnavailable.
			switch code {
			case 400:
				if !errors.Is(err, domain.ErrBackendRejected) {
					t.Errorf("code %d: want ErrBackendRejected, got %v", code, err)
				}
			default:
				if !errors.Is(err, domain.ErrBackendUnavailable) {
					t.Errorf("code %d: want ErrBackendUnavailable, got %v", code, err)
				}
			}
		})
	}
}

func TestRecommendMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`not-json`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	_, err := c.Recommend(context.Background(), domain.Query{Text: "q"})
	if !errors.Is(err, domain.ErrBackendRejected) {
		t.Errorf("error = %v, want ErrBackendRejected", err)
	}
}

func TestRecommendContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.Write([]byte(`{"output":"late"}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := c.Recommend(ctx, domain.Query{Text: "q"})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context deadline exceeded or canceled", err)
	}
}

func TestRecommendCustomPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"output":"ok"}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{
		BaseURL:       srv.URL,
		HeaderName:    "X-K",
		HeaderValue:   "v",
		RecommendPath: "/custom/recommend",
	})

	_, err := c.Recommend(context.Background(), domain.Query{Text: "q"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/custom/recommend" {
		t.Errorf("path = %q, want /custom/recommend", gotPath)
	}
}
// --- Ingest ---

func TestIngestSuccess(t *testing.T) {
	var gotBody map[string]any
	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true,"book_id":"6ba7b810-9dad-11d1-80b4-00c04fd430c8"}`))
	}))
	defer srv.Close()

	c, err := n8n.New(n8n.Config{
		BaseURL:     srv.URL,
		HeaderName:  "X-N8n-Auth",
		HeaderValue: "s3cret",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	book := domain.CatalogBook{
		ID:            "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		ISBN:          "978-0134494166",
		Title:         "Clean Architecture",
		Author:        "Robert C. Martin",
		PublishedYear: 2017,
		Description:   "A developers guide",
	}

	if err := c.Ingest(context.Background(), book); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPath != n8n.DefaultIngestPath {
		t.Errorf("path = %q, want %q", gotPath, n8n.DefaultIngestPath)
	}

	// The payload carries the bibliographic record the workflow indexes.
	if gotBody["book_id"] != book.ID {
		t.Errorf("book_id = %v, want %q", gotBody["book_id"], book.ID)
	}
	if gotBody["title"] != book.Title {
		t.Errorf("title = %v, want %q", gotBody["title"], book.Title)
	}
	if gotBody["author"] != book.Author {
		t.Errorf("author = %v, want %q", gotBody["author"], book.Author)
	}
	if gotBody["source"] != "ai-service" {
		t.Errorf("source = %v, want ai-service", gotBody["source"])
	}
	// An ISBN is passed as a tag so the workflow can index it.
	tags, ok := gotBody["tags"].([]any)
	if !ok || len(tags) != 1 || tags[0] != book.ISBN {
		t.Errorf("tags = %v, want [%q]", gotBody["tags"], book.ISBN)
	}
	// Content falls back to the rendered bibliographic line.
	if content, _ := gotBody["content"].(string); !strings.Contains(content, book.Title) {
		t.Errorf("content = %v, want it to contain the title", gotBody["content"])
	}
}

func TestIngestWithoutISBN(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	book := domain.CatalogBook{ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "No ISBN"}
	if err := c.Ingest(context.Background(), book); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, present := gotBody["tags"]; present {
		t.Errorf("tags should be absent without an ISBN, got %v", gotBody["tags"])
	}
}

func TestIngestOKFalseIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"ok":false,"book_id":"6ba7b810-9dad-11d1-80b4-00c04fd430c8"}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	err := c.Ingest(context.Background(), domain.CatalogBook{
		ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "X",
	})
	if !errors.Is(err, domain.ErrBackendRejected) {
		t.Errorf("error = %v, want ErrBackendRejected", err)
	}
}

func TestIngestMissingOKFieldIsSuccess(t *testing.T) {
	// A 200 without an ok field is treated as success: only an explicit
	// ok=false is a reported failure.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"book_id":"6ba7b810-9dad-11d1-80b4-00c04fd430c8"}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	if err := c.Ingest(context.Background(), domain.CatalogBook{
		ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "X",
	}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestIngestServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("boom"))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{BaseURL: srv.URL, HeaderName: "X-K", HeaderValue: "v"})

	err := c.Ingest(context.Background(), domain.CatalogBook{
		ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "X",
	})
	if !errors.Is(err, domain.ErrBackendUnavailable) {
		t.Errorf("error = %v, want ErrBackendUnavailable", err)
	}
}

func TestIngestCustomPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c, _ := n8n.New(n8n.Config{
		BaseURL:     srv.URL,
		HeaderName:  "X-K",
		HeaderValue: "v",
		IngestPath:  "/custom/ingest",
	})

	if err := c.Ingest(context.Background(), domain.CatalogBook{
		ID: "6ba7b810-9dad-11d1-80b4-00c04fd430c8", Title: "X",
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/custom/ingest" {
		t.Errorf("path = %q, want /custom/ingest", gotPath)
	}
}

// --- base url handling ---

func TestBaseURLTrailingSlashIsTrimmed(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"output":"ok"}`))
	}))
	defer srv.Close()

	// A base URL with a trailing slash must not produce a double slash path.
	c, _ := n8n.New(n8n.Config{
		BaseURL:     srv.URL + "/",
		HeaderName:  "X-K",
		HeaderValue: "v",
	})

	if _, err := c.Recommend(context.Background(), domain.Query{Text: "q"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != n8n.DefaultRecommendPath {
		t.Errorf("path = %q, want %q", gotPath, n8n.DefaultRecommendPath)
	}
}

func TestUnreachableBackendIsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing listens here anymore

	c, _ := n8n.New(n8n.Config{BaseURL: url, HeaderName: "X-K", HeaderValue: "v"})

	_, err := c.Recommend(context.Background(), domain.Query{Text: "q"})
	if !errors.Is(err, domain.ErrBackendUnavailable) {
		t.Errorf("error = %v, want ErrBackendUnavailable", err)
	}
}