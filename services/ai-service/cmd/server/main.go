// Command server starts the AI Service: the assistant of the library.
//
// The process owns three surfaces and one background worker:
//
//   - a gRPC server with the ai.v1.AiService API, authenticated through
//     user-service and documented by reflection;
//   - a REST surface served by grpc-gateway from the same proto contract, plus
//     the Swagger UI of that contract;
//   - the catalog consumer, which keeps the vector index in step with the books
//     book-service publishes.
//
// Everything the assistant knows comes from the RAG workflow of ADR-0002, so the
// binary starts only when the workflow, the vector store and both upstream
// services are configured: a service that answers from an empty index would be
// worse than one that refuses to start.
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

	"github.com/sapelyuk/smart-library/pkg/events"
	amqpevents "github.com/sapelyuk/smart-library/pkg/events/amqp"
	"github.com/sapelyuk/smart-library/pkg/logger"
	aiv1 "github.com/sapelyuk/smart-library/services/ai-service/gen/go/ai/v1"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/auth"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/bookclient"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/config"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/consumer"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/handler"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/n8n"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/ragstore"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/usersvc"
)

// healthService matches the proto package of the API.
const healthService = "ai.v1.AiService"

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("ai-service: bad configuration", "error", err)
		os.Exit(1)
	}

	log, err := logger.New(logger.Options{Level: cfg.LogLevel, Format: cfg.LogFormat})
	if err != nil {
		slog.Error("ai-service: bad logger configuration", "error", err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil {
		log.Error("ai-service stopped", "error", err)
		os.Exit(1)
	}

	log.Info("ai-service stopped")
}

// run wires the service and serves it until a signal arrives.
func run(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The vector store is the memory of the assistant: without it a recommendation
	// cannot be grounded, so the pool is opened before anything else and a failure
	// here stops the startup.
	db, err := ragstore.Open(cfg.RAGStoreDSN, cfg.RAGStoreMaxOpenConns, cfg.RAGStoreMaxIdleConns,
		cfg.RAGStoreConnMaxLifetime)
	if err != nil {
		return err
	}

	defer db.Close()

	store := ragstore.NewStore(db)

	// user-service resolves the bearer tokens of the callers. A dial failure is
	// fatal: without authentication there is no authorization.
	userConn, err := grpc.NewClient(cfg.UserGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial user-service %s: %w", cfg.UserGRPCAddr, err)
	}

	defer userConn.Close()

	// book-service owns the bibliographic record an index entry is built from, so
	// the service reads a book back from it instead of trusting a request.
	bookConn, err := grpc.NewClient(cfg.CatalogGRPCAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial book-service %s: %w", cfg.CatalogGRPCAddr, err)
	}

	defer bookConn.Close()

	// The workflow implements both halves of the RAG cycle: the answer of a query
	// and the chunks written for one book.
	workflow, err := n8n.New(n8n.Config{
		BaseURL:     cfg.N8NBaseURL,
		HeaderName:  cfg.N8NHeaderName,
		HeaderValue: cfg.N8NHeaderValue,
		Timeout:     cfg.N8NTimeout,
	})
	if err != nil {
		return fmt.Errorf("build n8n client: %w", err)
	}

	aiSvc, err := service.New(workflow, workflow, store, bookclient.New(bookConn), service.Config{
		Logger: log,
	})
	if err != nil {
		return fmt.Errorf("build ai service: %w", err)
	}

	if cfg.ConsumerEnabled {
		transport, err := openBroker(cfg, log)
		if err != nil {
			return err
		}

		defer transport.Close()

		go func() {
			if err := consumer.Run(ctx, transport, aiSvc, log); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("catalog consumer stopped", "error", err)
			}
		}()
	}

	listener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.GRPCAddr, err)
	}

	grpcServer, healthServer := startGRPC(aiSvc, userConn, log)

	httpServer, loopback, err := startREST(cfg, listener.Addr())
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
		log.Info("ai-service REST listening", "addr", cfg.HTTPAddr, "swagger", swaggerURL(cfg.HTTPAddr))

		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err

			return
		}

		serveErr <- nil
	}()

	log.Info("ai-service listening", "grpc", listener.Addr().String())

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		log.Info("shutdown signal received", "timeout", cfg.ShutdownTimeout.String())

		shutdownREST(httpServer, loopback, cfg.ShutdownTimeout, log)
		shutdownGRPC(grpcServer, healthServer, cfg.ShutdownTimeout, log)

		return nil
	}
}

// openBroker builds the transport the catalog consumer subscribes with. The
// service overrides the broker defaults only when the environment names a
// value, so one RabbitMQ of the platform serves every service.
func openBroker(cfg config.Config, log *slog.Logger) (*amqpevents.Transport, error) {
	broker := events.ConfigFromEnv()

	if cfg.RabbitMQURL != "" {
		broker.URL = cfg.RabbitMQURL
	}

	if cfg.RabbitMQExchange != "" {
		broker.Exchange = cfg.RabbitMQExchange
	}

	transport, err := amqpevents.New(broker, log)
	if err != nil {
		return nil, fmt.Errorf("open broker: %w", err)
	}

	return transport, nil
}

// startGRPC builds the gRPC server of the service with its interceptor, health
// service and reflection.
func startGRPC(aiSvc *service.Service, userConn *grpc.ClientConn, log *slog.Logger) (*grpc.Server, *health.Server) {
	server := grpc.NewServer(
		grpc.UnaryInterceptor(auth.NewUnaryServerInterceptor(usersvc.NewVerifier(userConn))),
	)

	aiv1.RegisterAiServiceServer(server, handler.NewGRPCServer(aiSvc, log))

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(server, healthServer)
	healthServer.SetServingStatus(healthService, healthpb.HealthCheckResponse_SERVING)

	// Reflection keeps grpcurl and other generic clients usable during development.
	reflection.Register(server)

	return server, healthServer
}

// startREST serves the REST and Swagger surface of the service on top of its
// own gRPC API, which is the same path a gateway would take.
func startREST(cfg config.Config, grpcAddr net.Addr) (*http.Server, *grpc.ClientConn, error) {
	target := localTarget(grpcAddr)

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", target, err)
	}

	restHandler, err := handler.NewREST(aiv1.NewAiServiceClient(conn))
	if err != nil {
		conn.Close()

		return nil, nil, err
	}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           withHealthEndpoint(restHandler),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		// The recommendation endpoint runs an agent loop behind the gateway, so
		// the write deadline has to survive a slow workflow.
		WriteTimeout: 90 * time.Second,
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
// such as ":8085" or "[::]:8085" cannot be dialed directly.
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

// shutdownGRPC stops the gRPC server, first withdrawing readiness so that the
// gateway drains its connections.
func shutdownGRPC(server *grpc.Server, healthServer *health.Server, timeout time.Duration, log *slog.Logger) {
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
