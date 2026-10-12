// Command server starts the Notification Service: a minimal gRPC server plus
// the REST and Swagger endpoints generated from the proto contract.
//
// The real work of the service — consuming loan.* events and sending email
// reminders — happens in a background goroutine that subscribes to RabbitMQ.
// The gRPC/REST surface is thin: a healthcheck and placeholder endpoints that
// keep the gateway routing table consistent.
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
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"

	pkgconfig "github.com/sapelyuk/smart-library/pkg/config"
	pkgevents "github.com/sapelyuk/smart-library/pkg/events"
	eventamqp "github.com/sapelyuk/smart-library/pkg/events/amqp"
	"github.com/sapelyuk/smart-library/pkg/logger"
	notificationv1 "github.com/sapelyuk/smart-library/services/notification-service/gen/go/notification/v1"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/consumer"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/email"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/handler"
	"github.com/sapelyuk/smart-library/services/notification-service/internal/service"
)

// healthService matches the proto package of the API.
const healthService = "notification.v1.NotificationService"

type appConfig struct {
	grpcAddr        string
	httpAddr        string
	logLevel        string
	logFormat       string
	shutdownTimeout time.Duration

	smtpHost     string
	smtpPort     int
	smtpUsername string
	smtpPassword string
	smtpFrom     string
}

func loadConfig() appConfig {
	smtpPort := pkgconfig.Int("NOTIFICATION_SERVICE_SMTP_PORT", 587)

	return appConfig{
		grpcAddr:        pkgconfig.String("NOTIFICATION_SERVICE_GRPC_ADDR", ":8084"),
		httpAddr:        pkgconfig.String("NOTIFICATION_SERVICE_HTTP_ADDR", ":8094"),
		logLevel:        pkgconfig.String("NOTIFICATION_SERVICE_LOG_LEVEL", "info"),
		logFormat:       pkgconfig.String("NOTIFICATION_SERVICE_LOG_FORMAT", "json"),
		shutdownTimeout: pkgconfig.Duration("NOTIFICATION_SERVICE_SHUTDOWN_TIMEOUT", 15*time.Second),
		smtpHost:        pkgconfig.String("NOTIFICATION_SERVICE_SMTP_HOST", ""),
		smtpPort:        smtpPort,
		smtpUsername:    pkgconfig.String("NOTIFICATION_SERVICE_SMTP_USERNAME", ""),
		smtpPassword:    pkgconfig.String("NOTIFICATION_SERVICE_SMTP_PASSWORD", ""),
		smtpFrom:        pkgconfig.String("NOTIFICATION_SERVICE_SMTP_FROM", ""),
	}
}

func main() {
	cfg := loadConfig()

	log, err := logger.New(logger.Options{Level: cfg.logLevel, Format: cfg.logFormat})
	if err != nil {
		slog.Error("notification-service: bad logger configuration", "error", err)
		os.Exit(1)
	}

	if err := run(cfg, log); err != nil {
		log.Error("notification-service stopped", "error", err)
		os.Exit(1)
	}

	log.Info("notification-service stopped")
}

func run(cfg appConfig, log *slog.Logger) error {
	if cfg.smtpHost == "" {
		return errors.New("NOTIFICATION_SERVICE_SMTP_HOST is required")
	}

	if cfg.smtpUsername == "" && cfg.smtpPassword == "" {
		return errors.New("NOTIFICATION_SERVICE_SMTP_USERNAME and NOTIFICATION_SERVICE_SMTP_PASSWORD are required")
	}

	if cfg.smtpFrom == "" {
		return errors.New("NOTIFICATION_SERVICE_SMTP_FROM is required")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Build the event transport. The notification service only consumes events,
	// so it only needs a Subscriber; publishing is not required.
	transport, err := eventamqp.New(pkgevents.ConfigFromEnv(), log)
	if err != nil {
		return fmt.Errorf("build event transport: %w", err)
	}

	defer transport.Close()

	// Build the SMTP sender. If the transport is unavailable the service still
	// starts: delivery failures are logged and retried on next message.
	sender := email.New(email.Config{
		Host:     cfg.smtpHost,
		Port:     cfg.smtpPort,
		Username: cfg.smtpUsername,
		Password: cfg.smtpPassword,
		From:     cfg.smtpFrom,
	}, log)

	// Build the notification service.
	notificationSvc := service.New(sender, log)

	// Start the consumer: it subscribes to loan.* events and processes them.
	// The subscription blocks, so it runs in a goroutine.
	go consumer.Run(ctx, transport, notificationSvc, log)

	// --- gRPC server (thin: healthcheck + placeholder endpoints) ---
	grpcServer := grpc.NewServer()

	notificationv1.RegisterNotificationServiceServer(grpcServer, handler.NewGRPCServer(log))

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus(healthService, healthpb.HealthCheckResponse_SERVING)

	// Reflection keeps grpcurl and other generic clients usable during development.
	reflection.Register(grpcServer)

	listener, err := net.Listen("tcp", cfg.grpcAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.grpcAddr, err)
	}

	// --- HTTP server (REST + Swagger surface) ---
	restHandler, err := handler.NewREST(cfg.grpcAddr)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.httpAddr,
		Handler:           restHandler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	serveErr := make(chan error, 1)

	go func() {
		log.Info("notification-service listening", "addr", cfg.grpcAddr)

		if err := grpcServer.Serve(listener); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			serveErr <- err
			return
		}

		serveErr <- nil
	}()

	go func() {
		log.Info("notification-service REST listening", "addr", cfg.httpAddr, "swagger", swaggerURL(cfg.httpAddr))

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

		shutdownHTTP(httpServer, cfg.shutdownTimeout, log)
		shutdownGRPC(grpcServer, healthServer, cfg.shutdownTimeout, log)

		return nil
	}
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

// shutdownHTTP stops the HTTP server gracefully.
func shutdownHTTP(server *http.Server, timeout time.Duration, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Warn("http shutdown failed", "error", err)
	}
}

// shutdownGRPC stops the gRPC server gracefully.
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
