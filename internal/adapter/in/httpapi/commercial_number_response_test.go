package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

func responseSale(t *testing.T) *model.Sale {
	t.Helper()
	s, err := model.RegisterSale("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333",
		time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		[]model.LineInput{{DetailID: "44444444-4444-4444-8444-444444444444", ProductID: "55555555-5555-4555-8555-555555555555", Quantity: 1, UnitPriceCents: 100}})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSaleResponseExposesCommercialNumberKeepingSaleID(t *testing.T) {
	s := responseSale(t)
	if err := s.AssignCommercialNumber("VEN-2026-000009"); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(saleResponse(s))
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["commercialNumber"] != "VEN-2026-000009" || body["saleId"] != s.ID() {
		t.Fatalf("number and internal UUID must both be present: %s", raw)
	}
}

func TestSaleResponseOmitsCommercialNumberForSalesWithoutOne(t *testing.T) {
	raw, err := json.Marshal(saleResponse(responseSale(t)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "commercialNumber") {
		t.Fatalf("a missing number must be absent, never fabricated or null: %s", raw)
	}
}

// Running out of numbers for the year is a business rejection, not an unexpected failure: the closed error
// catalog (07-api/guidelines.md) has no other code for it, 409 is not used, 503 is the Gateway's, and the Workflow
// only treats 400/422 with a valid envelope as proof that the registration did not commit (ADR-015) - a 500
// would leave the stock reservation in place.
func TestYearlySequenceExhaustionIsAnIdentifiableBusinessRuleRejection(t *testing.T) {
	for _, err := range []error{model.ErrCommercialSequenceExhausted, fmt.Errorf("register: %w", model.ErrCommercialSequenceExhausted)} {
		status, code, message, details := statusFor(err)
		if status != 422 || code != "BUSINESS_RULE_VIOLATION" || message != "The yearly commercial number sequence is exhausted" ||
			len(details) != 1 || details[0].Field != "commercialNumber" || details[0].Message != "yearly sequence exhausted" {
			t.Fatalf("exhaustion mapped to %d %s %q %v", status, code, message, details)
		}
	}
}

func TestRegistrationAtTheYearlyCeilingAnswersAnEnvelopeThatLeaksNothing(t *testing.T) {
	f := newHTTPFixture(t)
	f.repo.failure = fmt.Errorf("secret-database: %w", model.ErrCommercialSequenceExhausted)
	w := f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register")
	assertHTTPError(t, w, 422, "BUSINESS_RULE_VIOLATION")
	var body errorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.TraceID == "" || len(body.Details) != 1 {
		t.Fatalf("envelope: %s %v", w.Body.String(), err)
	}
	for _, internal := range []string{"sale_counter", "VEN-", "mongo", "secret-database"} {
		if strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(internal)) {
			t.Fatalf("internal detail %q leaked: %s", internal, w.Body.String())
		}
	}
}
