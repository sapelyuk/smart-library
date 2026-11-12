// Package tenant resolves the single default tenant used during Phase 1.
package tenant

import (
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"
)

const (
	EnvDefaultTenantID = "BOOK_SERVICE_DEFAULT_TENANT_ID"
	DefaultID          = "00000000-0000-0000-0000-000000000001"
)

type Resolver struct {
	id uuid.UUID
}

func NewResolverFromEnv() (*Resolver, error) {
	id := strings.TrimSpace(os.Getenv(EnvDefaultTenantID))
	if id == "" {
		id = DefaultID
	}

	parsed, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("tenant: parse %s: %w", EnvDefaultTenantID, err)
	}
	if parsed == uuid.Nil {
		return nil, fmt.Errorf("tenant: %s must not be the zero UUID", EnvDefaultTenantID)
	}

	return &Resolver{id: parsed}, nil
}

func (r *Resolver) ID() uuid.UUID {
	return r.id
}
