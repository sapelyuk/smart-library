package tenant_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/sapelyuk/smart-library/services/book-service/internal/tenant"
)

func TestNewResolverFromEnvUsesDefault(t *testing.T) {
	t.Setenv(tenant.EnvDefaultTenantID, "")

	resolver, err := tenant.NewResolverFromEnv()
	if err != nil {
		t.Fatalf("NewResolverFromEnv: %v", err)
	}
	if got := resolver.ID().String(); got != tenant.DefaultID {
		t.Fatalf("tenant ID = %q, want %q", got, tenant.DefaultID)
	}
}

func TestNewResolverFromEnvReadsTenantID(t *testing.T) {
	const tenantID = "9ee8f5da-7d51-49fb-9836-411198e48ef1"
	t.Setenv(tenant.EnvDefaultTenantID, tenantID)

	resolver, err := tenant.NewResolverFromEnv()
	if err != nil {
		t.Fatalf("NewResolverFromEnv: %v", err)
	}
	if got := resolver.ID(); got != uuid.MustParse(tenantID) {
		t.Fatalf("tenant ID = %s, want %s", got, tenantID)
	}
}

func TestNewResolverFromEnvRejectsInvalidTenantID(t *testing.T) {
	t.Setenv(tenant.EnvDefaultTenantID, "not-a-uuid")

	if _, err := tenant.NewResolverFromEnv(); err == nil {
		t.Fatal("NewResolverFromEnv(invalid ID) = nil error, want error")
	}
}
