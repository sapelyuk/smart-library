package domain

import "errors"

// Domain errors of the loan service. Wrap them with fmt.Errorf("%w") on the way
// up and match with errors.Is in the transport layer.
var (
	ErrNotFound = errors.New("loan not found")

	ErrInvalidReaderID = errors.New("reader id must not be empty")
	ErrInvalidBookID   = errors.New("book id must not be empty")
	ErrInvalidCopyID   = errors.New("copy id must not be empty")

	ErrInvalidLoanPeriod = errors.New("invalid loan period")
	ErrInvalidExtension  = errors.New("invalid renewal extension")
	ErrAlreadyReturned   = errors.New("loan is already returned")

	// ErrNoAvailableCopy is reported by book-service when a book has no free
	// copy to lend.
	ErrNoAvailableCopy = errors.New("book has no available copies")

	// ErrBookNotFound is reported by book-service when the book does not exist.
	ErrBookNotFound = errors.New("book not found")

	// ErrCopyNotFound is reported by book-service when the copy does not exist.
	ErrCopyNotFound = errors.New("copy not found")

	// ErrCopyNotAvailable is reported by book-service when the copy cannot be
	// returned because it is not on loan.
	ErrCopyNotAvailable = errors.New("copy is not on loan")

	// ErrCopyAlreadyOnLoan is the storage-level guard: the copy already has an
	// open loan, so it cannot be lent again.
	ErrCopyAlreadyOnLoan = errors.New("copy is already on loan")

	// Authorization failures raised by the domain rules of Principal.
	ErrPermissionDenied = errors.New("permission denied")
	ErrUnauthenticated  = errors.New("authentication required")
)
