// Command server запускает API Gateway — единый HTTP-вход для клиентских приложений.
//
// Шлюз принимает HTTP/JSON-запросы, проверяет JWT-токен и маршрутизирует вызовы
// к book-service и user-service через grpc-gateway (HTTP-эндпоинты сервисов).
//
// Конфигурация — только переменные окружения (см. godoc пакета main).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sapelyuk/smart-library/pkg/logger"
	"github.com/sapelyuk/smart-library/services/api-gateway/internal/auth"
	"github.com/sapelyuk/smart-library/services/api-gateway/internal/proxy"
)

// config — конфигурация шлюза, только из переменных окружения.
type config struct {
	httpAddr        string        // HTTP_ADDR — адрес HTTP-шлюза (по умолчанию ":8080")
	bookServiceURL  string        // BOOK_SERVICE_URL — HTTP-адрес book-service (grpc-gateway)
	userServiceURL  string        // USER_SERVICE_URL — HTTP-адрес user-service (grpc-gateway)
	shutdownTimeout time.Duration // SHUTDOWN_TIMEOUT — время на graceful shutdown
	logLevel        string        // LOG_LEVEL — debug|info|warn|error
	logFormat       string        // LOG_FORMAT — json|text
}

// loadConfig читает конфигурацию из переменных окружения.
func loadConfig() (config, error) {
	cfg := config{
		httpAddr:        getenv("HTTP_ADDR", ":8080"),
		bookServiceURL:  os.Getenv("BOOK_SERVICE_URL"),
		userServiceURL:  os.Getenv("USER_SERVICE_URL"),
		shutdownTimeout: 15 * time.Second,
		logLevel:        getenv("LOG_LEVEL", "info"),
		logFormat:       getenv("LOG_FORMAT", "json"),
	}

	if v := os.Getenv("SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return config{}, fmt.Errorf("invalid SHUTDOWN_TIMEOUT: %w", err)
		}

		cfg.shutdownTimeout = d
	}

	if cfg.bookServiceURL == "" {
		return config{}, errors.New("BOOK_SERVICE_URL is required")
	}

	if cfg.userServiceURL == "" {
		return config{}, errors.New("USER_SERVICE_URL is required")
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}

	return fallback
}

func main() {
	logLevel := flag.String("log-level", "", "override LOG_LEVEL: debug|info|warn|error")
	logFormat := flag.String("log-format", "", "override LOG_FORMAT: json|text")
	flag.Parse()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-gateway: invalid config: %v\n", err)
		os.Exit(1)
	}

	if *logLevel != "" {
		cfg.logLevel = *logLevel
	}

	if *logFormat != "" {
		cfg.logFormat = *logFormat
	}

	log, err := logger.New(logger.Options{Level: cfg.logLevel, Format: cfg.logFormat})
	if err != nil {
		fmt.Fprintf(os.Stderr, "api-gateway: %v\n", err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("api-gateway stopped with error", "error", err)
		os.Exit(1)
	}
}

func run(cfg config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Разбираем URL бэкендов.
	bookURL, err := url.Parse(cfg.bookServiceURL)
	if err != nil {
		return fmt.Errorf("invalid BOOK_SERVICE_URL: %w", err)
	}

	userURL, err := url.Parse(cfg.userServiceURL)
	if err != nil {
		return fmt.Errorf("invalid USER_SERVICE_URL: %w", err)
	}

	// Создаём прокси.
	bookProxy := proxy.New(bookURL, log)
	userProxy := proxy.New(userURL, log)

	// Маршрутизатор без авторизации — только публичные пути.
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	// Публичные пути аутентификации (login, register) — без JWT.
	publicMux.Handle("/v1/auth/login", userProxy.Handler())
	publicMux.Handle("/v1/auth/register", userProxy.Handler())

	// Маршрутизатор с авторизацией.
	privateMux := http.NewServeMux()

	// user-service: все остальные /v1/users/ и /v1/auth/ (me, change-password).
	privateMux.Handle("/v1/users/", userProxy.Handler())
	privateMux.Handle("/v1/auth/", userProxy.Handler())

	// book-service: каталог книг и выдача.
	privateMux.Handle("/v1/books/", bookProxy.Handler())
	privateMux.Handle("/v1/borrow/", bookProxy.Handler())

	// Создаём аутентификатор.
	authenticator := auth.NewAuthenticator()

	// Оборачиваем маршрутизаторы в middleware.
	// Сначала проверяем публичные пути, затем приватные через authenticator.
	combinedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Health check и публичные пути — без авторизации.
		if path == "/healthz" || path == "/v1/auth/login" || path == "/v1/auth/register" {
			publicMux.ServeHTTP(w, r)

			return
		}

		// Остальные пути — через аутентификатор.
		authenticator.Middleware(privateMux).ServeHTTP(w, r)
	})

	srv := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           combinedHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		log.Info("api-gateway started listening", "addr", cfg.httpAddr)

		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		log.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}

		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown timed out", "error", err)
		srv.Close()

		return err
	}

	log.Info("api-gateway stopped cleanly")

	return nil
}
