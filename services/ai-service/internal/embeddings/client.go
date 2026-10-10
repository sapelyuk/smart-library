// Package embeddings is the client of the model that turns text into vectors.
//
// The vector width has to match the width the store was built with, so the
// expected dimension is part of the configuration and a mismatch is reported
// instead of being written into the index. The endpoint is the OpenAI-compatible
// /embeddings route, which Google's OpenAI-compatible surface, OpenAI itself and
// a local server all speak — the RAG workflow uses Gemini embeddings, and this
// client has to produce vectors of the same space for retrieval to be meaningful.
package embeddings

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

// defaultTimeout bounds one embedding call: a query embedding is a single small
// request, so it is bounded tightly and a slow provider is an outage, not a
// reason to hold a reader's request open.
const defaultTimeout = 15 * time.Second

// Config describes the embeddings endpoint.
type Config struct {
	// BaseURL is the root of the API, e.g.
	// https://generativelanguage.googleapis.com/v1beta/openai. The embeddings
	// path is appended to it.
	BaseURL string

	// APIKey is sent as a bearer token; the OpenAI-compatible APIs all read it
	// that way.
	APIKey string

	// Model is the embedding model, e.g. gemini-embedding-001.
	Model string

	// Dimensions is the width the vectors must have to be comparable with what
	// the index holds. Zero skips the check, which only makes sense when the
	// provider truncates on request and the store was sized the same way.
	Dimensions int

	// Path overrides the /embeddings route when the provider nests it deeper.
	Path string

	Timeout time.Duration

	// HTTPClient is used when set; tests inject the client of httptest.
	HTTPClient *http.Client
}

// Client calls the embeddings endpoint.
type Client struct {
	http       *http.Client
	endpoint   string
	apiKey     string
	model      string
	dimensions int
}

var _ service.Embedder = (*Client)(nil)

// New builds the client and validates the configuration.
func New(cfg Config) (*Client, error) {
	raw := strings.TrimSpace(cfg.BaseURL)
	if raw == "" {
		return nil, fmt.Errorf("embeddings: base url is required")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("embeddings: bad base url %q: %w", raw, err)
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("embeddings: base url must be http or https, got %q", parsed.Scheme)
	}

	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("embeddings: model is required")
	}

	if cfg.Dimensions < 0 {
		return nil, fmt.Errorf("embeddings: dimensions must not be negative")
	}

	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		path = "/embeddings"
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/" + strings.TrimLeft(path, "/")

	return &Client{
		http:       httpClient,
		endpoint:   parsed.String(),
		apiKey:     strings.TrimSpace(cfg.APIKey),
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
	}, nil
}

// embedRequest is the OpenAI-compatible request body.
type embedRequest struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

// embedResponse carries one vector per input, in the order the inputs came.
type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

// Embed turns one piece of text into a vector.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("embeddings: text is empty")
	}

	body, err := json.Marshal(embedRequest{Model: c.model, Input: text})
	if err != nil {
		return nil, fmt.Errorf("embeddings: marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embeddings: build request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		return nil, fmt.Errorf("%w: embeddings call: %w", domain.ErrBackendUnavailable, err)
	}

	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: read embeddings answer: %w", domain.ErrBackendUnavailable, err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return nil, fmt.Errorf("%w: embeddings answered %d%s",
				domain.ErrBackendUnavailable, resp.StatusCode, snippet(raw))
		}

		return nil, fmt.Errorf("%w: embeddings answered %d%s",
			domain.ErrBackendRejected, resp.StatusCode, snippet(raw))
	}

	var reply embedResponse
	if err := json.Unmarshal(raw, &reply); err != nil {
		return nil, fmt.Errorf("%w: decode embeddings answer: %w", domain.ErrBackendRejected, err)
	}

	// A provider may answer 200 with an error object inside the body.
	if reply.Error != nil {
		return nil, fmt.Errorf("%w: embeddings: %s", domain.ErrBackendRejected, reply.Error.Message)
	}

	if len(reply.Data) == 0 || len(reply.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("%w: embeddings answered without a vector", domain.ErrBackendRejected)
	}

	vector := reply.Data[0].Embedding

	// The width is checked here rather than left to the database: a vector of the
	// wrong width would be rejected row by row, and the operator would read a
	// storage error instead of "you configured a different model than the index".
	if c.dimensions > 0 && len(vector) != c.dimensions {
		return nil, fmt.Errorf("%w: model %s returned %d dimensions, the index holds %d",
			domain.ErrBackendUnavailable, c.model, len(vector), c.dimensions)
	}

	return vector, nil
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
