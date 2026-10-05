// Command server starts the Book Service: a gRPC server plus the REST and
// Swagger endpoints generated from the same proto contract.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	pkgconfig "github.com/sapelyuk/smart-library/pkg/config"
	"github.com/sapelyuk/smart-library/pkg/logger"
	"github.com/sapelyuk/smart-library/pkg/migrate"
	bookv1 "github.com/sapelyuk/smart-library/services/book-service/gen/go/book/v1"
	"github.com/sapelyuk/smart-library/services/book-service/internal/handler"
	"github.com/sapelyuk/smart-library/services/book-service/internal/repository/postgres"
	"github.com/sapelyuk/smart-library/services/book-service/internal/service"
	"github.com/sapelyuk/smart-library/services/book-service/migrations"
)

// healthService matches the proto package of the API.
const healthService = "book.v1.BookService"

type appConfig struct {
	grpcAddr        string
	httpAddr        string
	logLevel        string
	logFormat       string
	shutdownTimeout time.Duration
	// Database configuration. The service requires a PostgreSQL DSN: there is
	// no in-memory fallback in production code.
	dbDSN             string
	dbMigrate         bool
	dbMaxOpenConns    int
	dbMaxIdleConns    int
	dbConnMaxLifetime time.Duration
}

func loadConfig() appConfig {
	return appConfig{
		grpcAddr:          pkgconfig.String("BOOK_SERVICE_GRPC_ADDR", ":8081"),
		httpAddr:          pkgconfig.String("BOOK_SERVICE_HTTP_ADDR", ":8091"),
		logLevel:          pkgconfig.String("BOOK_SERVICE_LOG_LEVEL", "info"),
		logFormat:         pkgconfig.String("BOOK_SERVICE_LOG_FORMAT", "json"),
		shutdownTimeout:   pkgconfig.Duration("BOOK_SERVICE_SHUTDOWN_TIMEOUT", 15*time.Second),
		dbDSN:             pkgconfig.String("BOOK_SERVICE_DB_DSN", ""),
		dbMigrate:         pkgconfig.Bool("BOOK_SERVICE_DB_MIGRATE", true),
		dbMaxOpenConns:    pkgconfig.Int("BOOK_SERVICE_DB_MAX_OPEN_CONNS", 25),
		dbMaxIdleConns:    pkgconfig.Int("BOOK_SERVICE_DB_MAX_IDLE_CONNS", 5),
		dbConnMaxLifetime: pkgconfig.Duration("BOOK_SERVICE_DB_CONN_MAX_LIFETIME", 30*time.Minute),
	}
}

func main() {
	cfg := loadConfig()

	log, err := logger.New(logger.Options{Level: cfg.logLevel, Format: cfg.logFormat})
	if err != nil {
		slog.Error("book-service: bad logger configuration", "error", err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil {
		log.Error("book-service stopped", "error", err)
		os.Exit(1)
	}

	log.Info("book-service stopped")
}

func run(cfg appConfig, log *slog.Logger) error {
	if cfg.dbDSN == "" {
		return errors.New("BOOK_SERVICE_DB_DSN is required")
	}

	db, err := postgres.Open(cfg.dbDSN, cfg.dbMaxOpenConns, cfg.dbMaxIdleConns, cfg.dbConnMaxLifetime)
	if err != nil {
		return err
	}

	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.dbMigrate {
		applied, err := migrate.Apply(ctx, db, migrations.FS, ".", log)
		if err != nil {
			return err
		}

		log.Info("migrations checked", "applied", applied)
	}

	store := postgres.NewStore(db)

	log.Info("book-service listening", "addr", cfg.grpcAddr, "storage", "postgresql")

	bookService := service.NewBookService(store, store)

	grpcServer := grpc.NewServer()
	bookv1.RegisterBookServiceServer(grpcServer, handler.NewGRPCServer(bookService, log))

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus(healthService, healthpb.HealthCheckResponse_SERVING)

	// Reflection keeps grpcurl and other generic clients usable during development.
	reflection.Register(grpcServer)

	listener, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.grpcAddr, err)
	}

	httpServer, conn, err := startREST(cfg, listener.Addr())
	if err != nil {
		return err
	}

	serveErr := make(chan error, 1)

	go func() {
		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err

			return
		}

		serveErr <- nil
	}()

	go func() {
		log.Info("book-service REST listening", "addr", cfg.httpAddr, "swagger", swaggerURL(cfg.httpAddr))

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err

			return
		}

		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.shutdownTimeout.String())

		shutdownREST(httpServer, conn, cfg.shutdownTimeout, log)
		shutdownGRPC(grpcServer, healthServer, cfg.shutdownTimeout, log)

		return nil
	}
}

// startREST serves the REST and Swagger surface of the service on top of its
// own gRPC API, which is the same path a gateway would take.
func startREST(cfg appConfig, grpcAddr net.Addr) (*http.Server, *grpc.ClientConn, error) {
	target := localTarget(grpcAddr)

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", target, err)
	}

	restHandler, err := handler.NewREST(bookv1.NewBookServiceClient(conn))
	if err != nil {
		conn.Close()

		return nil, nil, err
	}

	server := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           withHealthEndpoint(restHandler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	return server, conn, nil
}

// withHealthEndpoint adds the liveness endpoint that the container healthcheck
// probes. The REST handler is a gRPC-gateway mux that owns every other path, so
// the health path is intercepted before it reaches the gateway.
func withHealthEndpoint(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")

			return
		}

		next.ServeHTTP(w, r)
	})
}

// localTarget converts a listen address into a dialable one: wildcard hosts
// such as ":8081" or "[::]:8081" cannot be dialed directly.
func localTarget(addr net.Addr) string {
	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return addr.String()
	}

	if host == "" || host == "::" || host == "0.0.0.0" || host == "*" {
		host = "127.0.0.1"
	}

	return net.JoinHostPort(host, port)
}

// swaggerURL renders the documentation address for the startup log.
func swaggerURL(httpAddr string) string {
	host, port, err := net.SplitHostPort(httpAddr)
	if err != nil {
		return "http://localhost" + httpAddr + "/swagger/"
	}

	if host == "" {
		host = "localhost"
	}

	return "http://" + net.JoinHostPort(host, port) + "/swagger/"
}

// shutdownREST stops the HTTP listener and releases the loopback gRPC client.
func shutdownREST(server *http.Server, conn *grpc.ClientConn, timeout time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Warn("http shutdown failed", "error", err)
	}

	if err := conn.Close(); err != nil {
		log.Warn("closing grpc client failed", "error", err)
	}
}

func shutdownGRPC(server *grpc.Server, healthServer *health.Server, timeout time.Duration, log *slog.Logger) {
	// Stop advertising readiness first so that the gateway drains connections.
	healthServer.SetServingStatus(healthService, healthpb.HealthCheckResponse_NOT_SERVING)

	stopped := make(chan struct{})

	go func() {
		server.GracefulStop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(timeout):
		server.Stop()
		log.Warn("graceful shutdown timed out, forcing close")
	}
}
