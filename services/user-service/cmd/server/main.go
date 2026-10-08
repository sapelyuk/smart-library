// Command server starts the User Service: a gRPC server plus the REST and
// Swagger endpoints generated from the same proto contract.
//
// Every value comes from the environment, prefixed USER_SERVICE_*, so the binary
// is configured the same way in a shell, in a compose file and in a deployment
// manifest.
package main

import (
	"context"
	"database/sql"
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
	pkgevents "github.com/sapelyuk/smart-library/pkg/events"
	eventamqp "github.com/sapelyuk/smart-library/pkg/events/amqp"
	"github.com/sapelyuk/smart-library/pkg/logger"
	"github.com/sapelyuk/smart-library/pkg/migrate"
	userv1 "github.com/sapelyuk/smart-library/services/user-service/gen/go/user/v1"
	"github.com/sapelyuk/smart-library/services/user-service/internal/consumer"
	"github.com/sapelyuk/smart-library/services/user-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/user-service/internal/handler"
	"github.com/sapelyuk/smart-library/services/user-service/internal/repository/postgres"
	"github.com/sapelyuk/smart-library/services/user-service/internal/security"
	"github.com/sapelyuk/smart-library/services/user-service/internal/service"
	"github.com/sapelyuk/smart-library/services/user-service/migrations"
)

// healthService matches the proto package of the API.
const healthService = "user.v1.UserService"

type appConfig struct {
	grpcAddr        string
	httpAddr        string
	logLevel        string
	logFormat       string
	shutdownTimeout time.Duration

	dsn        string
	migrate    bool
	sessionTTL time.Duration
	minPassLen int
	purgeEvery time.Duration

	seedEmail    string
	seedPassword string
	seedFullName string
	seedPhone    string
}

func loadConfig() appConfig {
	dsn, err := pkgconfig.RequireString("USER_SERVICE_DB_DSN")
	if err != nil {
		slog.Error("user-service: environment", "error", err)
		os.Exit(1)
	}

	return appConfig{
		grpcAddr:        pkgconfig.String("USER_SERVICE_GRPC_ADDR", ":8082"),
		httpAddr:        pkgconfig.String("USER_SERVICE_HTTP_ADDR", ":8092"),
		logLevel:        pkgconfig.String("USER_SERVICE_LOG_LEVEL", "info"),
		logFormat:       pkgconfig.String("USER_SERVICE_LOG_FORMAT", "json"),
		shutdownTimeout: pkgconfig.Duration("USER_SERVICE_SHUTDOWN_TIMEOUT", 15*time.Second),

		dsn:        dsn,
		migrate:    pkgconfig.String("USER_SERVICE_DB_MIGRATE", "true") == "true",
		sessionTTL: pkgconfig.Duration("USER_SERVICE_SESSION_TTL", 24*time.Hour),
		minPassLen: pkgconfig.Int("USER_SERVICE_PASSWORD_MIN_LENGTH", 12),
		purgeEvery: pkgconfig.Duration("USER_SERVICE_PURGE_INTERVAL", 15*time.Minute),

		seedEmail:    pkgconfig.String("USER_SERVICE_SEED_LIBRARIAN_EMAIL", ""),
		seedPassword: pkgconfig.String("USER_SERVICE_SEED_LIBRARIAN_PASSWORD", ""),
		seedFullName: pkgconfig.String("USER_SERVICE_SEED_LIBRARIAN_FULL_NAME", "Head Librarian"),
		seedPhone:    pkgconfig.String("USER_SERVICE_SEED_LIBRARIAN_PHONE", ""),
	}
}

func main() {
	cfg := loadConfig()

	log, err := logger.New(logger.Options{Level: cfg.logLevel, Format: cfg.logFormat})
	if err != nil {
		slog.Error("user-service: bad logger configuration", "error", err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil {
		log.Error("user-service stopped", "error", err)
		os.Exit(1)
	}

	log.Info("user-service stopped")
}

func run(cfg appConfig, log *slog.Logger) error {
	db, err := postgres.Open(cfg.dsn, 16, 4, time.Hour)
	if err != nil {
		return err
	}

	defer db.Close()

	if err := db.Ping(); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.migrate {
		applied, err := migrate.Apply(ctx, db, migrations.FS, ".", log)
		if err != nil {
			return err
		}

		log.Info("migrations checked", "applied", applied)
	}

	users := postgres.NewStore(db)
	sessions := postgres.NewSessionStore(db)

	// Domain events travel through RabbitMQ (ADR-0001). The transport connects
	// lazily and reconnects on its own, so a broker that is down at startup does
	// not stop authentication; the events published while it is down are lost,
	// which the best-effort async path accepts.
	transport, err := eventamqp.New(pkgevents.ConfigFromEnv(), log)
	if err != nil {
		return fmt.Errorf("build event transport: %w", err)
	}

	defer transport.Close()

	publisher, err := pkgevents.NewEventPublisher(transport)
	if err != nil {
		return fmt.Errorf("build event publisher: %w", err)
	}

	userService, err := service.New(users, sessions, publisher, service.Config{
		SessionTTL: cfg.sessionTTL,
		PasswordPolicy: domain.PasswordPolicy{
			MinLength: cfg.minPassLen,
		},
		Logger: log,
	})
	if err != nil {
		return err
	}

	if err := seedLibrarian(ctx, db, cfg, log); err != nil {
		return err
	}

	go purgeLoop(ctx, userService, cfg.purgeEvery, log)

	// Placeholder notification consumer until Notification Service (#48) owns
	// real delivery: it keeps the subscribe path exercised end to end.
	go consumer.RunNotificationStub(ctx, transport, log)

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(handler.NewAuthUnaryInterceptor(userService)),
	)

	userv1.RegisterUserServiceServer(grpcServer, handler.New(userService, log))

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
		log.Info("user-service listening", "addr", cfg.grpcAddr, "storage", "postgresql")

		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err

			return
		}

		serveErr <- nil
	}()

	go func() {
		log.Info("user-service REST listening", "addr", cfg.httpAddr, "swagger", swaggerURL(cfg.httpAddr))

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err

			return
		}

		serveErr <- nil
	}()

	select {
	case err := <-serveErr:
		shutdownREST(httpServer, conn, cfg.shutdownTimeout, log)

		return err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.shutdownTimeout.String())

		shutdownREST(httpServer, conn, cfg.shutdownTimeout, log)
		shutdownGRPC(grpcServer, healthServer, cfg.shutdownTimeout, log)

		return nil
	}
}

// seedLibrarian creates the first staff account.
//
// A librarian may only be created by a librarian, so the very first one has to
// arrive from outside the API. Without this the system could be registered into
// but never administered. Both variables are optional: an empty email leaves the
// service with no seeded account, which is what a production deployment wants
// after the first administrator has been created for real.
func seedLibrarian(ctx context.Context, db *sql.DB, cfg appConfig, log *slog.Logger) error {
	if cfg.seedEmail == "" || cfg.seedPassword == "" {
		log.Info("no seed librarian configured")

		return nil
	}

	hash, err := security.HashPassword(cfg.seedPassword)
	if err != nil {
		return err
	}

	librarian, err := domain.NewUser(cfg.seedEmail, hash, cfg.seedFullName, cfg.seedPhone, domain.RoleLibrarian)
	if err != nil {
		return fmt.Errorf("seed librarian: %w", err)
	}

	now := time.Now().UTC()
	librarian.CreatedAt = now
	librarian.UpdatedAt = now

	store := postgres.NewStore(db)

	if err := store.Create(ctx, librarian); err != nil {
		if errors.Is(err, domain.ErrEmailAlreadyExists) {
			log.Info("seed librarian already present", "email", cfg.seedEmail)

			return nil
		}

		return fmt.Errorf("seed librarian: %w", err)
	}

	log.Info("seed librarian created", "email", cfg.seedEmail, "user_id", librarian.ID)

	return nil
}

// purgeLoop deletes sessions that outlived their validity. The read path drops a
// session the moment an expired token is presented, so this only keeps the table
// from growing with tokens nobody presents again.
func purgeLoop(ctx context.Context, svc *service.Service, every time.Duration, log *slog.Logger) {
	if every <= 0 {
		return
	}

	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := svc.PurgeExpiredSessions(ctx); err != nil {
				log.Warn("cannot purge expired sessions", "error", err)
			}
		}
	}
}

// startREST serves the REST and Swagger surface of the service on top of its own
// gRPC API, which is the same path a gateway would take.
func startREST(cfg appConfig, grpcAddr net.Addr) (*http.Server, *grpc.ClientConn, error) {
	target := localTarget(grpcAddr)

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", target, err)
	}

	restHandler, err := handler.NewREST(userv1.NewUserServiceClient(conn))
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

// localTarget converts a listen address into a dialable one: wildcard hosts such
// as ":8082" or "[::]:8082" cannot be dialed directly.
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
