// Package domain holds the vocabulary of the AI service: the query a reader
// asks, the recommendation the service answers with, the slice of the catalogue
// the index holds, and the caller the use cases authorize.
//
// The package has no dependencies on transports or adapters: everything that
// talks to n8n, to the vector store or to book-service implements a port the
// service layer declares, and the domain stays testable on its own.
package domain

import "errors"

// Domain errors of the AI service. Wrap them with fmt.Errorf("%w") on the way up
// and match with errors.Is in the transport layer.
var (
	// ErrEmptyQuery reports a query without any text.
	ErrEmptyQuery = errors.New("query must not be empty")

	// ErrQueryTooLong reports a query above the limit the backend is fed.
	ErrQueryTooLong = errors.New("query is too long")

	// ErrInvalidSessionID reports a conversation id outside the safe character
	// set. The id is echoed into the memory of the backend, so it is validated
	// rather than passed through.
	ErrInvalidSessionID = errors.New("session id must be a short alphanumeric token")

	// ErrInvalidBookID reports a book id that is not a catalog identifier.
	ErrInvalidBookID = errors.New("book id must be a valid catalog identifier")

	// ErrBookNotFound is reported by the catalog port when the book does not
	// exist: indexing it is impossible, so the index keeps the stale copy (if
	// any) and the caller decides whether to drop it.
	ErrBookNotFound = errors.New("book not found in the catalog")

	// ErrBackendUnavailable reports that the RAG backend could not answer: it
	// is down, its workflow is not active, its credentials are wrong or its
	// provider quota is spent. It is the operator's error, not the caller's.
	ErrBackendUnavailable = errors.New("ai backend is unavailable")

	// ErrBackendRejected reports that the backend answered 4xx to a request the
	// service built: a contract mismatch on our side rather than an outage.
	ErrBackendRejected = errors.New("ai backend rejected the request")

	// ErrIndexUnavailable reports that the vector store could not be reached.
	ErrIndexUnavailable = errors.New("book index is unavailable")

	// ErrPermissionDenied is raised by the authorization rules of Principal.
	ErrPermissionDenied = errors.New("permission denied")

	// ErrUnauthenticated reports a request without a resolved caller.
	ErrUnauthenticated = errors.New("authentication required")
)
