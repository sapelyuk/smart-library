// Command server starts the API gateway: the single HTTP entry point in front of
// the services.
//
// A request arrives as HTTP/JSON, is authenticated by its bearer token and is
// forwarded to the REST surface of book-service or user-service, chosen by the
// prefix of its path. Clients therefore address one host instead of learning the
// address of every service.
//
// Every value comes from the environment, so the binary is configured the same way
// in a shell, in a compose file and in a deployment manifest.
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

// config is the configuration of the gateway, read entirely from the environment.
type config struct {
	httpAddr        string        // HTTP_ADDR: address the gateway listens on, ":8080" by default
	bookServiceURL  string        // BOOK_SERVICE_URL: REST address of book-service
	userServiceURL  string        // USER_SERVICE_URL: REST address of user-service
	shutdownTimeout time.Duration // SHUTDOWN_TIMEOUT: budget for the graceful shutdown
	logLevel        string        // LOG_LEVEL: debug|info|warn|error
	logFormat       string        // LOG_FORMAT: json|text
}

// loadConfig reads the configuration and refuses to start with a half of the
// backends unknown: a missing address would only surface as 502 responses later.
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

	// The addresses of the backends are parsed once, so a typo fails the startup.
	bookURL, err := url.Parse(cfg.bookServiceURL)
	if err != nil {
		return fmt.Errorf("invalid BOOK_SERVICE_URL: %w", err)
	}

	userURL, err := url.Parse(cfg.userServiceURL)
	if err != nil {
		return fmt.Errorf("invalid USER_SERVICE_URL: %w", err)
	}

	bookProxy := proxy.New(bookURL, log)
	userProxy := proxy.New(userURL, log)

	// The routes that answer without a session.
	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok"}`)
	})

	// Signing in and signing up are how a caller obtains a token, so they cannot
	// require one.
	publicMux.Handle("/v1/auth/login", userProxy.Handler())
	publicMux.Handle("/v1/auth/register", userProxy.Handler())

	// Everything else is forwarded only after the token has been checked.
	privateMux := http.NewServeMux()

	// Accounts and sessions belong to user-service, including the routes that
	// change a password and end the current session.
	privateMux.Handle("/v1/users/", userProxy.Handler())
	privateMux.Handle("/v1/auth/", userProxy.Handler())

	// The catalog and the copy inventory belong to book-service.
	privateMux.Handle("/v1/books/", bookProxy.Handler())
	privateMux.Handle("/v1/borrow/", bookProxy.Handler())

	authenticator := auth.NewAuthenticator()

	// The two routers are picked apart by path: the public paths go through as
	// they are, the rest is wrapped in the token check.
	combinedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path == "/healthz" || path == "/v1/auth/login" || path == "/v1/auth/register" {
			publicMux.ServeHTTP(w, r)

			return
		}

		authenticator.Middleware(privateMux).ServeHTTP(w, r)
	})

	// The timeouts are set on purpose: a gateway that waits forever for a client
	// that stopped reading holds a connection per request, which is the cheapest
	// way to run out of file descriptors.
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
