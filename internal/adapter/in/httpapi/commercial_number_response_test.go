package httpapi

import (
	"encoding/json"
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
