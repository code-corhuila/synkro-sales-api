package httpapi

import (
	"fmt"
	"testing"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

func TestStoredDomainFailuresRemainInternalErrors(t *testing.T) {
	f := newHTTPFixture(t)
	f.repo.failure = fmt.Errorf("secret-database stored invariant: %w", model.ErrAmountOverflow)
	assertHTTPError(t, f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register"), 500, "INTERNAL_ERROR")
	for _, path := range []string{"/api/v1/sales", "/api/v1/sales/" + ownerID} {
		assertHTTPError(t, f.request(t, "GET", path, "", ownerID, "ADMIN", "sales:read"), 500, "INTERNAL_ERROR")
	}
}
