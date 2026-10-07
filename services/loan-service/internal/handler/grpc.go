// Package handler adapts transports to the service layer: it parses requests,
// maps domain errors onto gRPC status codes and converts entities back to
// protobuf messages.
package handler

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	loanv1 "github.com/sapelyuk/smart-library/services/loan-service/gen/go/loan/v1"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/auth"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/service"
)

// GRPCServer implements loanv1.LoanServiceServer.
type GRPCServer struct {
	loanv1.UnimplementedLoanServiceServer

	service *service.Service
	log     *slog.Logger
}

var _ loanv1.LoanServiceServer = (*GRPCServer)(nil)

// NewGRPCServer builds the gRPC adapter of the loan service.
func NewGRPCServer(svc *service.Service, log *slog.Logger) *GRPCServer {
	return &GRPCServer{service: svc, log: log}
}

// Borrow lends the first available copy of a book to a reader.
func (s *GRPCServer) Borrow(ctx context.Context, req *loanv1.BorrowRequest) (*loanv1.BorrowResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("Borrow", err)
	}

	readerID, err := parseUUID(req.GetReaderId(), "reader_id")
	if err != nil {
		return nil, err
	}

	bookID, err := parseUUID(req.GetBookId(), "book_id")
	if err != nil {
		return nil, err
	}

	loan, err := s.service.Borrow(ctx, caller, service.BorrowInput{ReaderID: readerID, BookID: bookID})
	if err != nil {
		return nil, s.mapError("Borrow", err)
	}

	return &loanv1.BorrowResponse{Loan: toProtoLoan(loan)}, nil
}

// Return closes a loan and releases the copy.
func (s *GRPCServer) Return(ctx context.Context, req *loanv1.ReturnRequest) (*loanv1.ReturnResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("Return", err)
	}

	loanID, err := parseUUID(req.GetLoanId(), "loan_id")
	if err != nil {
		return nil, err
	}

	loan, err := s.service.Return(ctx, caller, loanID)
	if err != nil {
		return nil, s.mapError("Return", err)
	}

	return &loanv1.ReturnResponse{Loan: toProtoLoan(loan)}, nil
}

// Renew pushes the due date of an open loan forward.
func (s *GRPCServer) Renew(ctx context.Context, req *loanv1.RenewRequest) (*loanv1.RenewResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("Renew", err)
	}

	loanID, err := parseUUID(req.GetLoanId(), "loan_id")
	if err != nil {
		return nil, err
	}

	extension := time.Duration(req.GetExtendDays()) * 24 * time.Hour

	loan, err := s.service.Renew(ctx, caller, loanID, extension)
	if err != nil {
		return nil, s.mapError("Renew", err)
	}

	return &loanv1.RenewResponse{Loan: toProtoLoan(loan)}, nil
}

// Get returns a single loan.
func (s *GRPCServer) Get(ctx context.Context, req *loanv1.GetRequest) (*loanv1.Loan, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("Get", err)
	}

	loanID, err := parseUUID(req.GetLoanId(), "loan_id")
	if err != nil {
		return nil, err
	}

	loan, err := s.service.Get(ctx, caller, loanID)
	if err != nil {
		return nil, s.mapError("Get", err)
	}

	return toProtoLoan(loan), nil
}

// List returns a page of loans.
func (s *GRPCServer) List(ctx context.Context, req *loanv1.ListRequest) (*loanv1.ListResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("List", err)
	}

	filter, err := loanFilter(req)
	if err != nil {
		return nil, err
	}

	loans, total, err := s.service.List(ctx, caller, filter)
	if err != nil {
		return nil, s.mapError("List", err)
	}

	return &loanv1.ListResponse{Loans: toProtoLoans(loans), Total: int32(total)}, nil
}

// ListOverdue returns a page of overdue loans.
func (s *GRPCServer) ListOverdue(ctx context.Context, req *loanv1.ListOverdueRequest) (*loanv1.ListOverdueResponse, error) {
	caller, err := auth.PrincipalFrom(ctx)
	if err != nil {
		return nil, s.mapError("ListOverdue", err)
	}

	loans, total, err := s.service.ListOverdue(ctx, caller, int(req.GetLimit()), int(req.GetOffset()))
	if err != nil {
		return nil, s.mapError("ListOverdue", err)
	}

	return &loanv1.ListOverdueResponse{Loans: toProtoLoans(loans), Total: int32(total)}, nil
}

// mapError maps a domain error onto a gRPC status; unexpected failures are
// logged and reported as codes.Internal without leaking internals.
func (s *GRPCServer) mapError(method string, err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound),
		errors.Is(err, domain.ErrBookNotFound),
		errors.Is(err, domain.ErrCopyNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, domain.ErrAlreadyReturned),
		errors.Is(err, domain.ErrCopyAlreadyOnLoan),
		errors.Is(err, domain.ErrNoAvailableCopy),
		errors.Is(err, domain.ErrCopyNotAvailable):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, domain.ErrInvalidReaderID),
		errors.Is(err, domain.ErrInvalidBookID),
		errors.Is(err, domain.ErrInvalidCopyID),
		errors.Is(err, domain.ErrInvalidLoanPeriod),
		errors.Is(err, domain.ErrInvalidExtension):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, domain.ErrPermissionDenied):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, domain.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, err.Error())
	case errors.Is(err, context.Canceled):
		return status.FromContextError(err).Err()
	default:
		s.log.Error("unexpected failure", "method", method, "error", err)

		return status.Error(codes.Internal, "internal error")
	}
}

// loanFilter builds the domain filter from the request, validating the enums.
func loanFilter(req *loanv1.ListRequest) (domain.LoanFilter, error) {
	filter := domain.LoanFilter{
		Limit:  int(req.GetLimit()),
		Offset: int(req.GetOffset()),
	}

	if raw := req.GetReaderId(); raw != "" {
		id, err := parseUUID(raw, "reader_id")
		if err != nil {
			return domain.LoanFilter{}, err
		}

		filter.ReaderID = &id
	}

	if raw := req.GetBookId(); raw != "" {
		id, err := parseUUID(raw, "book_id")
		if err != nil {
			return domain.LoanFilter{}, err
		}

		filter.BookID = &id
	}

	statusValue, err := toDomainStatusFilter(req.GetStatus())
	if err != nil {
		return domain.LoanFilter{}, err
	}

	filter.Status = statusValue

	return filter, nil
}

// toDomainStatusFilter maps the request enum onto the effective status filter.
// UNSPECIFIED means "any status".
func toDomainStatusFilter(value loanv1.LoanStatus) (domain.EffectiveStatus, error) {
	switch value {
	case loanv1.LoanStatus_LOAN_STATUS_UNSPECIFIED:
		return "", nil
	case loanv1.LoanStatus_LOAN_STATUS_ACTIVE:
		return domain.StatusActive, nil
	case loanv1.LoanStatus_LOAN_STATUS_RETURNED:
		return domain.StatusReturned, nil
	case loanv1.LoanStatus_LOAN_STATUS_OVERDUE:
		return domain.StatusOverdue, nil
	default:
		return "", status.Errorf(codes.InvalidArgument, "unknown loan status: %d", value)
	}
}

func parseUUID(raw, field string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, status.Errorf(codes.InvalidArgument, "malformed %s: %v", field, err)
	}

	return parsed, nil
}

func toProtoLoans(loans []*domain.Loan) []*loanv1.Loan {
	protoLoans := make([]*loanv1.Loan, 0, len(loans))

	for _, loan := range loans {
		protoLoans = append(protoLoans, toProtoLoan(loan))
	}

	return protoLoans
}

func toProtoLoan(loan *domain.Loan) *loanv1.Loan {
	protoLoan := &loanv1.Loan{
		Id:         loan.ID.String(),
		ReaderId:   loan.ReaderID.String(),
		BookId:     loan.BookID.String(),
		CopyId:     loan.CopyID.String(),
		BorrowedAt: timestamppb.New(loan.BorrowedAt),
		DueAt:      timestamppb.New(loan.DueAt),
		Status:     toProtoStatus(loan.EffectiveStatus(time.Now().UTC())),
	}

	if loan.ReturnedAt != nil {
		protoLoan.ReturnedAt = timestamppb.New(*loan.ReturnedAt)
	}

	return protoLoan
}

func toProtoStatus(status domain.EffectiveStatus) loanv1.LoanStatus {
	switch status {
	case domain.StatusActive:
		return loanv1.LoanStatus_LOAN_STATUS_ACTIVE
	case domain.StatusReturned:
		return loanv1.LoanStatus_LOAN_STATUS_RETURNED
	case domain.StatusOverdue:
		return loanv1.LoanStatus_LOAN_STATUS_OVERDUE
	default:
		return loanv1.LoanStatus_LOAN_STATUS_UNSPECIFIED
	}
}
