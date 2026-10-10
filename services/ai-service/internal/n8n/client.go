// Package n8n is the HTTP client of the n8n workflow that implements the RAG
// cycle behind this service (ADR-0002).
//
// The workflow exposes two header-authenticated webhooks:
//
//	POST /webhook/book-rag/recommend  {"chatInput": "...", "sessionId": "..."}
//	POST /webhook/book-rag/ingest     {"book_id": "...", "title": "...", ...}
//
// Their exact payload shapes were read out of workflow/book-rag-system.json and
// are documented in rag/docs/USAGE.md. Nothing above this package is allowed to
// know that n8n exists: the client implements the service.Recommender and
// service.Indexer ports, and swapping the workflow for a native Go cycle is a
// change of this file only.
package n8n

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
)

// Defaults of the workflow as it is deployed.
const (
	DefaultRecommendPath = "/webhook/book-rag/recommend"
	DefaultIngestPath    = "/webhook/book-rag/ingest"

	// defaultTimeout is generous on purpose: one recommend call runs an agent
	// loop with retrieval and a chat-model round trip inside it.
	defaultTimeout = 60 * time.Second

	// maxResponseBytes caps how much of a webhook answer is read. The workflow
	// answers with one JSON object; anything beyond this is a misconfigured
	// endpoint or a redirect into a large document, not an answer.
	maxResponseBytes = 1 << 20
)

// Config describes the workflow endpoint the client talks to.
type Config struct {
	// BaseURL is the root of the n8n instance, e.g. http://localhost:5678.
	BaseURL string

	// HeaderName and HeaderValue are the header auth of the webhooks, the pair
	// n8n checks before the workflow runs.
	HeaderName  string
	HeaderValue string

	// RecommendPath and IngestPath override the default webhook paths.
	RecommendPath string
	IngestPath    string

	// Timeout bounds one webhook call; zero means defaultTimeout. An agent run
	// is slow by nature, so this is the request deadline, not a retry budget.
	Timeout time.Duration

	// HTTPClient is used when set; tests inject the client of httptest.
	HTTPClient *http.Client
}

// Client calls the webhooks of the RAG workflow.
type Client struct {
	http          *http.Client
	baseURL       string
	headerName    string
	headerValue   string
	recommendPath string
	ingestPath    string
}

// The client implements both halves of the RAG cycle the service layer expects.
var (
	_ service.Recommender = (*Client)(nil)
	_ service.Indexer     = (*Client)(nil)
)

// New builds the client. The base URL and the auth header are required: a client
// without them would call an open webhook, and the platform does not run one.
func New(cfg Config) (*Client, error) {
	raw := strings.TrimSpace(cfg.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("n8n: base url is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("n8n: bad base url %q: %w", raw, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("n8n: base url must be http or https, got %q", parsed.Scheme)
	}

	headerName := strings.TrimSpace(cfg.HeaderName)
	if headerName == "" {
		return nil, fmt.Errorf("n8n: webhook header name is required")
	}

	if strings.TrimSpace(cfg.HeaderValue) == "" {
		return nil, fmt.Errorf("n8n: webhook header value is required")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	return &Client{
		http:          httpClient,
		baseURL:       strings.TrimRight(parsed.String(), "/"),
		headerName:    headerName,
		headerValue:   cfg.HeaderValue,
		recommendPath: pathOrDefault(cfg.RecommendPath, DefaultRecommendPath),
		ingestPath:    pathOrDefault(cfg.IngestPath, DefaultIngestPath),
	}, nil
}

// Recommend runs the query through the agent and returns its answer.
//
// The workflow answers with prose only, so the books of the answer are left
// empty: the caller fills them from its own retrieval, which is the deterministic
// half of the pipeline.
func (c *Client) Recommend(ctx context.Context, query domain.Query) (domain.Answer, error) {
	var reply struct {
		Output    string `json:"output"`
		SessionID string `json:"sessionId"`
	}

	body := map[string]any{"chatInput": query.Text}
	if query.SessionID != "" {
		body["sessionId"] = query.SessionID
	}

	if err := c.post(ctx, c.recommendPath, body, &reply); err != nil {
		return domain.Answer{}, err
	}

	text := strings.TrimSpace(reply.Output)
	if text == "" {
		return domain.Answer{}, fmt.Errorf("%w: the recommend webhook answered without any text",
			domain.ErrBackendUnavailable)
	}

	return domain.Answer{
		Text:      text,
		SessionID: strings.TrimSpace(reply.SessionID),
		Source:    domain.SourceGenerated,
	}, nil
}

// Ingest hands one catalogue record to the indexing chain of the workflow.
//
// The chain is idempotent on book_id: it drops the previous chunks of the same
// identifier before writing the new ones, so re-sending a book updates it.
func (c *Client) Ingest(ctx context.Context, book domain.CatalogBook) error {
	var reply struct {
		OK     *bool  `json:"ok"`
		BookID string `json:"book_id"`
	}

	payload := map[string]any{
		"book_id":        book.ID,
		"title":          book.Title,
		"author":         book.Author,
		"published_year": book.PublishedYear,
		"description":    book.Description,
		"content":        book.IngestText(),
		"source":         "ai-service",
	}

	if book.ISBN != "" {
		payload["tags"] = []string{book.ISBN}
	}

	if err := c.post(ctx, c.ingestPath, payload, &reply); err != nil {
		return err
	}

	// The workflow answers {"ok": true, ...}; a 200 with ok false is a failure
	// the workflow reported politely, and treating it as success would leave the
	// caller believing a book is indexed when it is not.
	if reply.OK != nil && !*reply.OK {
		return fmt.Errorf("%w: the ingest webhook reported ok=false for book %s",
			domain.ErrBackendRejected, book.ID)
	}

	return nil
}

// post sends one JSON request and decodes one JSON response.
func (c *Client) post(ctx context.Context, path string, payload map[string]any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("n8n: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("n8n: build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set(c.headerName, c.headerValue)

	resp, err := c.http.Do(req)
	if err != nil {
		// A canceled caller context is the caller's decision, not an outage of
		// the backend: it must not be reported as one.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}

		return fmt.Errorf("%w: call %s: %w", domain.ErrBackendUnavailable, path, err)
	}

	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: read answer of %s: %w", domain.ErrBackendUnavailable, path, err)
	}

	if err := statusError(path, resp.StatusCode, raw); err != nil {
		return err
	}

	if out == nil {
		return nil
	}

	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%w: decode answer of %s: %w", domain.ErrBackendRejected, path, err)
	}

	return nil
}

// statusError translates a non-2xx webhook answer into a domain error.
func statusError(path string, code int, raw []byte) error {
	if code >= http.StatusOK && code < http.StatusMultipleChoices {
		return nil
	}

	detail := snippet(raw)

	switch {
	case code == http.StatusNotFound:
		// n8n answers 404 for a workflow that is not Active, which is the single
		// most common reason this service cannot answer.
		return fmt.Errorf("%w: %s answered 404, the workflow is probably not active%s",
			domain.ErrBackendUnavailable, path, detail)
	case code == http.StatusUnauthorized, code == http.StatusForbidden:
		return fmt.Errorf("%w: %s rejected the webhook credentials (%d)%s",
			domain.ErrBackendUnavailable, path, code, detail)
	case code == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s is rate limited (429)%s",
			domain.ErrBackendUnavailable, path, detail)
	case code >= http.StatusInternalServerError:
		return fmt.Errorf("%w: %s failed with %d%s", domain.ErrBackendUnavailable, path, code, detail)
	default:
		return fmt.Errorf("%w: %s failed with %d%s", domain.ErrBackendRejected, path, code, detail)
	}
}

// snippet renders the first characters of an error body for the log line.
func snippet(raw []byte) string {
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return ""
	}

	const max = 200
	if len(text) > max {
		text = text[:max] + "..."
	}

	return ": " + text
}

func pathOrDefault(path, fallback string) string {
	if trimmed := strings.TrimSpace(path); trimmed != "" {
		return "/" + strings.TrimPrefix(trimmed, "/")
	}

	return fallback
}
