// Package bookclient is the gRPC client the loan service uses to reserve and
// release physical copies in book-service.
//
// book-service owns the inventory: the loan service never touches its database,
// it asks over gRPC. A status error is translated into a domain error here so
// that the service layer keeps talking about domain concepts.
package bookclient

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bookv1 "github.com/sapelyuk/smart-library/services/book-service/gen/go/book/v1"
	"github.com/sapelyuk/smart-library/services/loan-service/internal/domain"
)

// Client wraps book.v1.BookService over an existing connection.
type Client struct {
	books bookv1.BookServiceClient
}

// New returns a client that calls book-service through conn.
func New(conn grpc.ClientConnInterface) *Client {
	return &Client{books: bookv1.NewBookServiceClient(conn)}
}

// BorrowCopy reserves the first available copy of the book and returns its id.
func (c *Client) BorrowCopy(ctx context.Context, bookID uuid.UUID) (uuid.UUID, error) {
	copy, err := c.books.BorrowBookCopy(ctx, &bookv1.BorrowBookCopyRequest{
		BookId: bookID.String(),
	})
	if err != nil {
		return uuid.Nil, translate(err, "borrow copy", domain.ErrBookNotFound, domain.ErrNoAvailableCopy)
	}

	id, err := uuid.Parse(copy.GetId())
	if err != nil {
		return uuid.Nil, fmt.Errorf("bookclient: borrow copy: malformed copy id %q: %w", copy.GetId(), err)
	}

	return id, nil
}

// ReturnCopy marks the copy available again.
func (c *Client) ReturnCopy(ctx context.Context, copyID uuid.UUID) error {
	_, err := c.books.ReturnBookCopy(ctx, &bookv1.ReturnBookCopyRequest{
		CopyId: copyID.String(),
	})
	if err != nil {
		return translate(err, "return copy", domain.ErrCopyNotFound, domain.ErrCopyNotAvailable)
	}

	return nil
}

// translate maps a status error of book-service onto a domain error. notFound
// and precondition name the domain errors that fit the operation that failed.
func translate(err error, action string, notFound, precondition error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("bookclient: %s: %w", action, err)
	}

	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("bookclient: %s: %w: %s", action, notFound, st.Message())
	case codes.FailedPrecondition:
		return fmt.Errorf("bookclient: %s: %w: %s", action, precondition, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("bookclient: %s: %w: %s", action, notFound, st.Message())
	default:
		return fmt.Errorf("bookclient: %s: %w", action, err)
	}
}
