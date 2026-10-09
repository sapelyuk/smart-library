// Package config reads the runtime configuration of the AI service from the
// environment. Every variable is prefixed AI_ so the service is unmistakably
// identified in logs, in a compose file and in a deployment manifest.
package config

import (
	"fmt"
	"time"

	pkgconfig "github.com/sapelyuk/smart-library/pkg/config"
)

// Config holds the runtime configuration of the service.
type Config struct {
	GRPCAddr        string
	HTTPAddr        string
	LogLevel        string
	LogFormat       string
	ShutdownTimeout time.Duration

	// Catalog — the gRPC address of book-service. Required.
	CatalogGRPCAddr string

	// User — the gRPC address of user-service. Required.
	UserGRPCAddr string

	// N8N — the RAG workflow that the service talks to for recommendations and
	// indexing. The base URL and the header auth are both required.
	N8NBaseURL     string
	N8NHeaderName  string
	N8NHeaderValue string
	N8NTimeout     time.Duration

	// Embeddings — optional; when empty the service answers from the n8n agent
	// only and the catalogue hits in the answer are left empty.
	EmbeddingsBaseURL    string
	EmbeddingsAPIKey     string
	EmbeddingsModel      string
	EmbeddingsDimensions int
	EmbeddingsTimeout    time.Duration

	// RAGStore — the pgvector database the index lives in.
	RAGStoreDSN             string
	RAGStoreMaxOpenConns    int
	RAGStoreMaxIdleConns    int
	RAGStoreConnMaxLifetime time.Duration

	// Consumer — whether the catalog event consumer runs on startup.
	ConsumerEnabled bool

	// RabbitMQ — overrides the defaults of pkg/events for the consumer queue.
	// Empty means the defaults: amqp://guest:guest@localhost:5672/ and
	// library.events.
	RabbitMQURL      string
	RabbitMQExchange string
}

// Load reads and validates the configuration.
func Load() (Config, error) {
	cfg := Config{
		GRPCAddr:        pkgconfig.String("AI_GRPC_ADDR", ":8085"),
		HTTPAddr:        pkgconfig.String("AI_HTTP_ADDR", ":8095"),
		LogLevel:        pkgconfig.String("AI_LOG_LEVEL", "info"),
		LogFormat:       pkgconfig.String("AI_LOG_FORMAT", "json"),
		ShutdownTimeout: pkgconfig.Duration("AI_SHUTDOWN_TIMEOUT", 15*time.Second),

		CatalogGRPCAddr: pkgconfig.String("AI_CATALOG_GRPC_ADDR", ""),
		UserGRPCAddr:    pkgconfig.String("AI_USER_GRPC_ADDR", ""),

		N8NBaseURL:     pkgconfig.String("AI_N8N_BASE_URL", ""),
		N8NHeaderName:  pkgconfig.String("AI_N8N_HEADER_NAME", "x-book-rag-key"),
		N8NHeaderValue: pkgconfig.String("AI_N8N_HEADER_VALUE", ""),
		N8NTimeout:     pkgconfig.Duration("AI_N8N_TIMEOUT", 60*time.Second),

		EmbeddingsBaseURL:    pkgconfig.String("AI_EMBEDDINGS_BASE_URL", ""),
		EmbeddingsAPIKey:     pkgconfig.String("AI_EMBEDDINGS_API_KEY", ""),
		EmbeddingsModel:      pkgconfig.String("AI_EMBEDDINGS_MODEL", "gemini-embedding-001"),
		EmbeddingsDimensions: pkgconfig.Int("AI_EMBEDDINGS_DIMENSIONS", 3072),
		EmbeddingsTimeout:    pkgconfig.Duration("AI_EMBEDDINGS_TIMEOUT", 15*time.Second),

		RAGStoreDSN:             pkgconfig.String("AI_RAG_STORE_DSN", ""),
		RAGStoreMaxOpenConns:    pkgconfig.Int("AI_RAG_STORE_MAX_OPEN_CONNS", 8),
		RAGStoreMaxIdleConns:    pkgconfig.Int("AI_RAG_STORE_MAX_IDLE_CONNS", 2),
		RAGStoreConnMaxLifetime: pkgconfig.Duration("AI_RAG_STORE_CONN_MAX_LIFETIME", 5*time.Minute),

		ConsumerEnabled:  pkgconfig.Bool("AI_CONSUMER_ENABLED", true),
		RabbitMQURL:      pkgconfig.String("AI_RABBITMQ_URL", ""),
		RabbitMQExchange: pkgconfig.String("AI_RABBITMQ_EXCHANGE", ""),
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks the required configuration.
func (c Config) Validate() error {
	if c.CatalogGRPCAddr == "" {
		return fmt.Errorf("AI_CATALOG_GRPC_ADDR is required")
	}

	if c.UserGRPCAddr == "" {
		return fmt.Errorf("AI_USER_GRPC_ADDR is required")
	}

	if c.N8NBaseURL == "" {
		return fmt.Errorf("AI_N8N_BASE_URL is required")
	}

	if c.N8NHeaderValue == "" {
		return fmt.Errorf("AI_N8N_HEADER_VALUE is required")
	}

	if c.RAGStoreDSN == "" {
		return fmt.Errorf("AI_RAG_STORE_DSN is required")
	}

	if c.EmbeddingsDimensions < 0 {
		return fmt.Errorf("AI_EMBEDDINGS_DIMENSIONS must not be negative")
	}

	if c.RAGStoreMaxOpenConns <= 0 {
		return fmt.Errorf("AI_RAG_STORE_MAX_OPEN_CONNS must be positive")
	}

	if c.RAGStoreMaxIdleConns < 0 {
		return fmt.Errorf("AI_RAG_STORE_MAX_IDLE_CONNS must not be negative")
	}

	return nil
}
