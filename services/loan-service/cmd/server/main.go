// Command server starts the Loan Service: a gRPC server plus the REST and
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
	loanv1 "github.com/sapelyuk/smart-library/services/loan-service/gen/go/loan/v1"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/auth"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/bookclient"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/events"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/handler"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/repository/postgres"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/service"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/usersvc"
	"github.com/sapelyuk/smart-library/services/loan-service/migrations"
)

// healthService matches the proto package of the API.
const healthService = "loan.v1.LoanService"

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
	// Address of book-service for the BorrowCopy / ReturnCopy calls.
	bookServiceAddr string
	// Address of user-service for token authentication.
	userServiceAddr string
	// Default lending period for new loans.
	loanPeriod time.Duration
}

func loadConfig() appConfig {
	return appConfig{
		grpcAddr:          pkgconfig.String("LOAN_SERVICE_GRPC_ADDR", ":8083"),
		httpAddr:          pkgconfig.String("LOAN_SERVICE_HTTP_ADDR", ":8093"),
		logLevel:          pkgconfig.String("LOAN_SERVICE_LOG_LEVEL", "info"),
		logFormat:         pkgconfig.String("LOAN_SERVICE_LOG_FORMAT", "json"),
		shutdownTimeout:   pkgconfig.Duration("LOAN_SERVICE_SHUTDOWN_TIMEOUT", 15*time.Second),
		dbDSN:             pkgconfig.String("LOAN_SERVICE_DB_DSN", ""),
		dbMigrate:         pkgconfig.Bool("LOAN_SERVICE_DB_MIGRATE", true),
		dbMaxOpenConns:    pkgconfig.Int("LOAN_SERVICE_DB_MAX_OPEN_CONNS", 25),
		dbMaxIdleConns:    pkgconfig.Int("LOAN_SERVICE_DB_MAX_IDLE_CONNS", 5),
		dbConnMaxLifetime: pkgconfig.Duration("LOAN_SERVICE_DB_CONN_MAX_LIFETIME", 30*time.Minute),
		bookServiceAddr:   pkgconfig.String("LOAN_SERVICE_BOOK_SERVICE_GRPC_ADDR", ""),
		userServiceAddr:   pkgconfig.String("LOAN_SERVICE_USER_SERVICE_GRPC_ADDR", ""),
		loanPeriod:        pkgconfig.Duration("LOAN_SERVICE_LOAN_PERIOD", 14*24*time.Hour),
	}
}

func main() {
	cfg := loadConfig()

	log, err := logger.New(logger.Options{Level: cfg.logLevel, Format: cfg.logFormat})
	if err != nil {
		slog.Error("loan-service: bad logger configuration", "error", err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil {
		log.Error("loan-service stopped", "error", err)
		os.Exit(1)
	}

	log.Info("loan-service stopped")
}

func run(cfg appConfig, log *slog.Logger) error {
	if cfg.dbDSN == "" {
		return errors.New("LOAN_SERVICE_DB_DSN is required")
	}

	if cfg.bookServiceAddr == "" {
		return errors.New("LOAN_SERVICE_BOOK_SERVICE_GRPC_ADDR is required")
	}

	if cfg.userServiceAddr == "" {
		return errors.New("LOAN_SERVICE_USER_SERVICE_GRPC_ADDR is required")
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

	// Connect to book-service: the loan service reserves and releases copies
	// through it. A dial failure is a fatal startup error because the service
	// cannot lend without the inventory.
	bookConn, err := grpc.NewClient(cfg.bookServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial book-service %s: %w", cfg.bookServiceAddr, err)
	}

	defer bookConn.Close()

	// Connect to user-service: the loan service resolves bearer tokens through
	// it. A dial failure is fatal: without authentication there is no
	// authorization.
	userConn, err := grpc.NewClient(cfg.userServiceAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		bookConn.Close()

		return fmt.Errorf("dial user-service %s: %w", cfg.userServiceAddr, err)
	}

	defer userConn.Close()

	// Build the loan service on top of the repository, the book inventory
	// client and the event publisher.
	publisher := events.NewLogPublisher(log)

	loanSvc, err := service.New(store, bookclient.New(bookConn), publisher, service.Config{
		LoanPeriod: cfg.loanPeriod,
		Logger:     log,
	})
	if err != nil {
		return fmt.Errorf("build loan service: %w", err)
	}

	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(auth.NewUnaryServerInterceptor(usersvc.NewVerifier(userConn))),
	)

	loanv1.RegisterLoanServiceServer(grpcServer, handler.NewGRPCServer(loanSvc, log))

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
		log.Info("loan-service REST listening", "addr", cfg.httpAddr, "swagger", swaggerURL(cfg.httpAddr))

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

	restHandler, err := handler.NewREST(loanv1.NewLoanServiceClient(conn))
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
// such as ":8083" or "[::]:8083" cannot be dialed directly.
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
