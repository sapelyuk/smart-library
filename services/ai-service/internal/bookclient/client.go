// Package bookclient reads the catalog of the library from book-service.
//
// book-service owns the bibliographic record, so this service never keeps its own
// copy of it and never indexes what it has not just read there. The projection is
// deliberately narrow: copy counts are inventory, and putting them into an index
// entry would only create a second, staler version of the truth.
package bookclient

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/sapelyuk/smart-library/services/ai-service/internal/domain"
	"github.com/sapelyuk/smart-library/services/ai-service/internal/service"
	bookv1 "github.com/sapelyuk/smart-library/services/book-service/gen/go/book/v1"
)

// Client reads catalog entries over gRPC.
type Client struct {
	client bookv1.BookServiceClient
}

var _ service.Catalog = (*Client)(nil)

// New builds the client on top of an established connection.
func New(conn grpc.ClientConnInterface) *Client {
	return &Client{client: bookv1.NewBookServiceClient(conn)}
}

// Book reads one catalog entry by its identifier.
//
// NOT_FOUND from book-service becomes domain.ErrBookNotFound: the caller asked to
// index a book the catalog does not have, which is a fact about the request rather
// than a transport failure.
func (c *Client) Book(ctx context.Context, bookID string) (domain.CatalogBook, error) {
	book, err := c.client.GetBook(ctx, &bookv1.GetBookRequest{Id: bookID})
	if err != nil {
		switch code := status.Code(err); code {
		case codes.NotFound:
			return domain.CatalogBook{}, fmt.Errorf("%w: %s", domain.ErrBookNotFound, bookID)
		case codes.InvalidArgument:
			return domain.CatalogBook{}, fmt.Errorf("%w: %s", domain.ErrInvalidBookID, bookID)
		}

		return domain.CatalogBook{}, fmt.Errorf("bookclient: read book %s: %w", bookID, err)
	}

	if book == nil {
		return domain.CatalogBook{}, fmt.Errorf("%w: %s", domain.ErrBookNotFound, bookID)
	}

	return domain.CatalogBook{
		ID:            book.GetId(),
		ISBN:          book.GetIsbn(),
		Title:         book.GetTitle(),
		Author:        book.GetAuthor(),
		Publisher:     book.GetPublisher(),
		PublishedYear: int(book.GetPublishedYear()),
	}, nil
}
