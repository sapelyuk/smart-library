// Package handler adapts transports to the service layer: it parses requests,
// maps domain errors onto gRPC status codes and converts entities back to
// protobuf messages.
package handler

import (
	"context"
	"errors"
	"log/slog"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	aiv1 "github.com/sapelyuk/smart-library/services/ai-service/gen/go/ai/v1"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/auth"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
)

// GRPCServer implements aiv1.AiServiceServer.
type GRPCServer struct {
	aiv1.UnimplementedAiServiceServer
	service *service.Service
	log     *slog.Logger
}

var _ aiv1.AiServiceServer = (*GRPCServer)(nil)

// NewGRPCServer builds the gRPC adapter of the AI service.
func NewGRPCServer(svc *service.Service, log *slog.Logger) *GRPCServer {
	return &GRPCServer{service: svc, log: log}
}

// Recommend answers a reader query.
func (s *GRPCServer) Recommend(ctx context.Context, req *aiv1.RecommendRequest) (*aiv1.RecommendResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("Recommend", err)
	}

	answer, err := s.service.Recommend(ctx, caller, service.RecommendInput{
		Query:     req.GetQuery(),
		SessionID: req.GetSessionId(),
		Limit:     int(req.GetLimit()),
	})
	if err != nil {
		return nil, s.mapError("Recommend", err)
	}

	return toProtoRecommendation(answer), nil
}

// IngestBook reindexes one catalog entry.
func (s *GRPCServer) IngestBook(ctx context.Context, req *aiv1.IngestBookRequest) (*aiv1.IngestBookResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("IngestBook", err)
	}

	book, err := s.service.IngestBook(ctx, caller, req.GetBookId())
	if err != nil {
		return nil, s.mapError("IngestBook", err)
	}

	return &aiv1.IngestBookResponse{
		BookId: book.ID,
		Title:  book.Title,
	}, nil
}

// DeleteBook drops one book from the vector index.
func (s *GRPCServer) DeleteBook(ctx context.Context, req *aiv1.DeleteBookRequest) (*aiv1.DeleteBookResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("DeleteBook", err)
	}

	deletion, err := s.service.DeleteBook(ctx, caller, req.GetBookId())
	if err != nil {
		return nil, s.mapError("DeleteBook", err)
	}

	return &aiv1.DeleteBookResponse{
		BookId:  deletion.BookID,
		Deleted: deletion.Deleted,
	}, nil
}

// IndexStatus reports the size of the vector index.
func (s *GRPCServer) IndexStatus(ctx context.Context, req *aiv1.IndexStatusRequest) (*aiv1.IndexStatusResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("IndexStatus", err)
	}

	stats, err := s.service.IndexStatus(ctx, caller)
	if err != nil {
		return nil, s.mapError("IndexStatus", err)
	}

	return &aiv1.IndexStatusResponse{
		IndexedBooks:  int32(stats.Books),
		IndexedChunks: int32(stats.Chunks),
	}, nil
}

// mapError maps a domain error onto a gRPC status; unexpected failures are
// logged and reported as codes.Internal without leaking internals.
func (s *GRPCServer) mapError(method string, err error) error {
	switch {
	case errors.Is(err, domain.ErrBookNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrInvalidBookID),
		errors.Is(err, domain.ErrEmptyQuery),
		errors.Is(err, domain.ErrQueryTooLong),
		errors.Is(err, domain.ErrInvalidSessionID):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrPermissionDenied):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, domain.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, domain.ErrBackendUnavailable),
		errors.Is(err, domain.ErrIndexUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, domain.ErrBackendRejected):
		s.log.Error("backend rejected the request", "method", method, "error", err)

		return status.Error(codes.Internal, "backend rejected the request")
	case errors.Is(err, context.Canceled):
		return status.FromContextError(err).Err()
	default:
		s.log.Error("unexpected failure", "method", method, "error", err)

		return status.Error(codes.Internal, "internal error")
	}
}

// toProtoRecommendation converts a domain.Answer into a protobuf recommendation.
func toProtoRecommendation(answer domain.Answer) *aiv1.RecommendResponse {
	books := make([]*aiv1.RecommendedBook, 0, len(answer.Books))

	for _, b := range answer.Books {
		books = append(books, &aiv1.RecommendedBook{
			BookId: b.BookID,
			Title:  b.Title,
			Author: b.Author,
		})
	}

	return &aiv1.RecommendResponse{
		Recommendation: &aiv1.Recommendation{
			Query:     "",
			Answer:    answer.Text,
			Books:     books,
			Source:    toProtoSource(answer.Source),
			SessionId: answer.SessionID,
		},
	}
}

// toProtoSource converts a domain.Source into the proto enum.
func toProtoSource(src domain.Source) aiv1.RecommendationSource {
	switch src {
	case domain.SourceGenerated:
		return aiv1.RecommendationSource_RECOMMENDATION_SOURCE_GENERATED
	case domain.SourceRetrieval:
		return aiv1.RecommendationSource_RECOMMENDATION_SOURCE_RETRIEVAL
	default:
		return aiv1.RecommendationSource_RECOMMENDATION_SOURCE_UNSPECIFIED
	}
}
