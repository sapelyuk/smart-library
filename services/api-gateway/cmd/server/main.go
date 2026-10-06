// Command server starts the API gateway: the single HTTP entry point in front of
// the services.
//
// A request arrives as HTTP/JSON, is authenticated by its bearer token (checked
// through user-service over gRPC AuthenticateToken) and is forwarded to the REST
// surface of book-service or user-service, chosen by the prefix of its path.
// Clients therefore address one host instead of learning the address of every
// service.
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

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pkgconfig "github.com/sapelyuk/smart-library/pkg/config"
	"github.com/sapelyuk/smart-library/pkg/logger"
	"github.com/sapelyuk/smart-library/services/api-gateway/internal/auth"
	"github.com/sapelyuk/smart-library/services/api-gateway/internal/proxy"
)

// config is the configuration of the gateway, read entirely from the environment.
type config struct {
	httpAddr            string        // GATEWAY_HTTP_ADDR: address the gateway listens on, ":8080" by default
	bookServiceURL      string        // GATEWAY_BOOK_SERVICE_URL: REST address of book-service
	userServiceURL      string        // GATEWAY_USER_SERVICE_URL: REST address of user-service
	userServiceGRPCAddr string        // GATEWAY_USER_SERVICE_GRPC_ADDR: gRPC address of user-service (for AuthenticateToken)
	shutdownTimeout     time.Duration // GATEWAY_SHUTDOWN_TIMEOUT: budget for the graceful shutdown
	logLevel            string        // GATEWAY_LOG_LEVEL: debug|info|warn|error
	logFormat           string        // GATEWAY_LOG_FORMAT: json|text
}

// loadConfig reads the configuration and refuses to start with a half of the
// backends unknown: a missing address would only surface as 502 responses later.
func loadConfig() (config, error) {
	cfg := config{
		httpAddr:            pkgconfig.String("GATEWAY_HTTP_ADDR", ":8080"),
		bookServiceURL:      pkgconfig.String("GATEWAY_BOOK_SERVICE_URL", ""),
		userServiceURL:      pkgconfig.String("GATEWAY_USER_SERVICE_URL", ""),
		userServiceGRPCAddr: pkgconfig.String("GATEWAY_USER_SERVICE_GRPC_ADDR", ""),
		shutdownTimeout:     pkgconfig.Duration("GATEWAY_SHUTDOWN_TIMEOUT", 15*time.Second),
		logLevel:            pkgconfig.String("GATEWAY_LOG_LEVEL", "info"),
		logFormat:           pkgconfig.String("GATEWAY_LOG_FORMAT", "json"),
	}

	if cfg.bookServiceURL == "" {
		return config{}, errors.New("GATEWAY_BOOK_SERVICE_URL is required")
	}

	if cfg.userServiceURL == "" {
		return config{}, errors.New("GATEWAY_USER_SERVICE_URL is required")
	}

	if cfg.userServiceGRPCAddr == "" {
		return config{}, errors.New("GATEWAY_USER_SERVICE_GRPC_ADDR is required")
	}

	return cfg, nil
}

func main() {
	logLevel := flag.String("log-level", "", "override GATEWAY_LOG_LEVEL: debug|info|warn|error")
	logFormat := flag.String("log-format", "", "override GATEWAY_LOG_FORMAT: json|text")
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
		return fmt.Errorf("invalid GATEWAY_BOOK_SERVICE_URL: %w", err)
	}

	userURL, err := url.Parse(cfg.userServiceURL)
	if err != nil {
		return fmt.Errorf("invalid GATEWAY_USER_SERVICE_URL: %w", err)
	}

	bookProxy := proxy.New(bookURL, log)
	userProxy := proxy.New(userURL, log)

	// Connect to user-service over gRPC so the gateway can call AuthenticateToken
	// and turn a bearer token into the Principal of the request.
	conn, err := grpc.NewClient(cfg.userServiceGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial user-service gRPC: %w", err)
	}

	defer conn.Close()

	verifier := auth.NewGRPCVerifier(conn)

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

	// Librarian-only routes: list and create users, deactivate and restore accounts,
	// and all write operations on the catalog. These are mounted behind RequireRole
	// so that a READER who happens to hold a valid token still cannot perform them.
	librarianMux := http.NewServeMux()
	librarianMux.Handle("/v1/users", userProxy.Handler())
	librarianMux.Handle("/v1/users/", userProxy.Handler())
	librarianMux.Handle("/v1/books", bookProxy.Handler())
	librarianMux.Handle("/v1/books/", bookProxy.Handler())

	// Everything else is forwarded only after the token has been checked.
	authenticatedMux := http.NewServeMux()
	authenticatedMux.Handle("/v1/users/", userProxy.Handler())
	authenticatedMux.Handle("/v1/auth/", userProxy.Handler())
	authenticatedMux.Handle("/v1/books/", bookProxy.Handler())
	authenticatedMux.Handle("/v1/borrow/", bookProxy.Handler())

	// The two routers are picked apart by path: the public paths go through as
	// they are, the rest is wrapped in the token check.
	combinedHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Health check and auth endpoints are always public.
		if path == "/healthz" || path == "/v1/auth/login" || path == "/v1/auth/register" {
			publicMux.ServeHTTP(w, r)

			return
		}

		// Librarian-only routes: exact match wins over prefix match in ServeMux.
		// "/v1/users" and "/v1/books" (without trailing slash) are librarian-only;
		// "/v1/users/{id}" and "/v1/books/{id}" fall through to authenticatedMux.
		if path == "/v1/users" || path == "/v1/books" {
			auth.Authenticator(verifier, auth.RequireRole("ROLE_LIBRARIAN")(librarianMux)).
				ServeHTTP(w, r)

			return
		}

		// All other routes require authentication but no specific role.
		auth.Authenticator(verifier, authenticatedMux).ServeHTTP(w, r)
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
