// Package handler holds the transport layer of the notification service:
// gRPC adapter and HTTP gateway (grpc-gateway) that expose the proto contract.
package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"

	notificationv1 "github.com/sapelyuk/smart-library/services/notification-service/gen/go/notification/v1"
)

// GRPCServer is the gRPC adapter that serves the notification API.
type GRPCServer struct {
	notificationv1.UnimplementedNotificationServiceServer
	log *slog.Logger
}

// NewGRPCServer builds the gRPC handler. The notification service has no
// business logic exposed through gRPC beyond healthcheck; the real work is
// done by the consumer goroutine.
func NewGRPCServer(log *slog.Logger) *GRPCServer {
	return &GRPCServer{log: log}
}

// Health is a no-op that satisfies the proto contract.
func (s *GRPCServer) Health(ctx context.Context, _ *notificationv1.Empty) (*notificationv1.Empty, error) {
	return &notificationv1.Empty{}, nil
}

// NewREST builds the REST handler (grpc-gateway mux) with a /healthz endpoint.
// The client is expected to talk to the gRPC server of this very process.
func NewREST(grpcAddr string) (http.Handler, error) {
	target := localTarget(grpcAddr)

	conn, err := grpc.NewClient(target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("dial gRPC %s: %w", target, err)
	}

	client := notificationv1.NewNotificationServiceClient(conn)

	gateway := runtime.NewServeMux(
		runtime.WithMarshalerOption(runtime.MIMEWildcard, &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{EmitUnpopulated: true},
		}),
	)

	if err := notificationv1.RegisterNotificationServiceHandlerClient(context.Background(), gateway, client); err != nil {
		conn.Close()
		return nil, fmt.Errorf("register gateway: %w", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, "OK")
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})

	mux.Handle("/", gateway)

	return mux, nil
}

// localTarget converts a listen address into a dialable one: wildcard hosts
// such as ":8084" or "[::]:8084" cannot be dialed directly.
func localTarget(addr string) string {
	// Try to split host:port; if addr has no host part, prepend localhost.
	if idx := len(addr) - 1; idx >= 0 && addr[idx] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}
