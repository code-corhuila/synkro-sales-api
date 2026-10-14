package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func saleFixture(t *testing.T, count int) *model.Sale {
	t.Helper()
	lines := make([]model.LineInput, count)
	for i := range lines {
		lines[i] = model.LineInput{DetailID: uuid.NewString(), ProductID: uuid.NewString(), Quantity: 2, UnitPriceCents: 1250}
	}
	s, err := model.RegisterSale(uuid.NewString(), uuid.NewString(), uuid.NewString(), time.Date(2026, 10, 11, 9, 30, 15, 123456789, time.FixedZone("Bogota", -18000)), lines)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func assertSale(t *testing.T, got, want *model.Sale) {
	t.Helper()
	if got == nil || got.ID() != want.ID() || got.CustomerID() != want.CustomerID() || got.CreatedBy() != want.CreatedBy() || got.Active() != want.Active() || got.TotalCents() != want.TotalCents() || !got.Date().Equal(time.UnixMilli(want.Date().UnixMilli())) || got.Date().Location() != time.UTC || !reflect.DeepEqual(got.Details(), want.Details()) {
		t.Fatalf("sale did not retain its complete persisted representation: got=%+v want=%+v", got, want)
	}
}

func assertRawFields(t *testing.T, raw bson.Raw, fields map[string]bson.Type) {
	t.Helper()
	elements, err := raw.Elements()
	if err != nil {
		t.Fatal(err)
	}
	if len(elements) != len(fields) {
		t.Fatalf("fields = %d, want %d", len(elements), len(fields))
	}
	for _, element := range elements {
		want, ok := fields[element.Key()]
		if !ok || element.Value().Type != want {
			t.Errorf("%s type = %v, want %v", element.Key(), element.Value().Type, want)
		}
	}
}

func assertSaleBSON(t *testing.T, raw bson.Raw, want *model.Sale, key string) {
	t.Helper()
	assertRawFields(t, raw, map[string]bson.Type{"_id": bson.TypeString, "customerId": bson.TypeString, "createdBy": bson.TypeString, "date": bson.TypeDateTime, "totalCents": bson.TypeInt64, "active": bson.TypeBoolean, "idempotencyKey": bson.TypeString, "details": bson.TypeArray})
	if raw.Lookup("_id").StringValue() != want.ID() || raw.Lookup("customerId").StringValue() != want.CustomerID() || raw.Lookup("createdBy").StringValue() != want.CreatedBy() || raw.Lookup("date").DateTime() != want.Date().UnixMilli() || raw.Lookup("totalCents").Int64() != want.TotalCents() || !raw.Lookup("active").Boolean() || raw.Lookup("idempotencyKey").StringValue() != key {
		t.Fatal("incorrect root BSON values")
	}
	values, err := raw.Lookup("details").Array().Values()
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != len(want.Details()) {
		t.Fatal("incorrect detail count")
	}
	for i, value := range values {
		d := value.Document()
		assertRawFields(t, d, map[string]bson.Type{"detailId": bson.TypeString, "productId": bson.TypeString, "quantity": bson.TypeInt32, "unitPriceCents": bson.TypeInt64, "subtotalCents": bson.TypeInt64, "active": bson.TypeBoolean})
		line := want.Details()[i]
		if d.Lookup("detailId").StringValue() != line.ID() || d.Lookup("productId").StringValue() != line.ProductID() || d.Lookup("quantity").Int32() != int32(line.Quantity()) || d.Lookup("unitPriceCents").Int64() != line.UnitPriceCents() || d.Lookup("subtotalCents").Int64() != line.SubtotalCents() || !d.Lookup("active").Boolean() {
			t.Fatal("incorrect frozen detail BSON values")
		}
	}
}

func TestSaleMappingRoundTrip(t *testing.T) {
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := saleFixture(t, count)
			raw, err := encodeSale("mapping-key", s)
			if err != nil {
				t.Fatal(err)
			}
			assertSaleBSON(t, raw, s, "mapping-key")
			got, err := decodeSale(raw)
			if err != nil {
				t.Fatal(err)
			}
			assertSale(t, got, s)
			if got.Date().Nanosecond() != 123000000 {
				t.Fatal("BSON milliseconds were not explicit")
			}
		})
	}
}

func validDocument(t *testing.T) bson.M {
	t.Helper()
	raw, err := encodeSale("fixture-key", saleFixture(t, 1))
	if err != nil {
		t.Fatal(err)
	}
	var doc bson.M
	if err := bson.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	// Driver v2 decodes documents inside interface-valued arrays as bson.D.
	for i, value := range doc["details"].(bson.A) {
		mapped := bson.M{}
		for _, field := range value.(bson.D) {
			mapped[field.Key] = field.Value
		}
		doc["details"].(bson.A)[i] = mapped
	}
	return doc
}

func firstDetail(doc bson.M) bson.M { return doc["details"].(bson.A)[0].(bson.M) }

func TestSaleMappingRejectsUnfaithfulStoredData(t *testing.T) {
	cases := map[string]func(bson.M){
		"total mismatch":       func(d bson.M) { d["totalCents"] = int64(1) },
		"subtotal mismatch":    func(d bson.M) { firstDetail(d)["subtotalCents"] = int64(1) },
		"inactive sale":        func(d bson.M) { d["active"] = false },
		"inactive detail":      func(d bson.M) { firstDetail(d)["active"] = false },
		"unknown root":         func(d bson.M) { d["status"] = "PAID" },
		"unknown detail":       func(d bson.M) { firstDetail(d)["discount"] = int64(1) },
		"missing field":        func(d bson.M) { delete(d, "date") },
		"missing detail field": func(d bson.M) { delete(firstDetail(d), "active") },
		"invalid uuid":         func(d bson.M) { d["customerId"] = "id-1" },
		"uppercase uuid":       func(d bson.M) { d["_id"] = strings.ToUpper("abcdefab-1234-1234-1234-123456789abc") },
		"invalid detail uuid":  func(d bson.M) { firstDetail(d)["detailId"] = "id-1" },
		"invalid product uuid": func(d bson.M) { firstDetail(d)["productId"] = "id-1" },
		"invalid creator uuid": func(d bson.M) { d["createdBy"] = "id-1" },
		"short key":            func(d bson.M) { d["idempotencyKey"] = "short" },
		"long key":             func(d bson.M) { d["idempotencyKey"] = strings.Repeat("k", 129) },
		"empty details":        func(d bson.M) { d["details"] = bson.A{} },
		"101 details": func(d bson.M) {
			a := bson.A{}
			for i := 0; i < 101; i++ {
				a = append(a, firstDetail(d))
			}
			d["details"] = a
		},
		"quantity long":       func(d bson.M) { firstDetail(d)["quantity"] = int64(2) },
		"money int32":         func(d bson.M) { d["totalCents"] = int32(2500) },
		"money double":        func(d bson.M) { firstDetail(d)["unitPriceCents"] = float64(1250) },
		"date string":         func(d bson.M) { d["date"] = "2026-10-11" },
		"null active":         func(d bson.M) { d["active"] = nil },
		"detail not document": func(d bson.M) { d["details"] = bson.A{"invalid"} },
		"zero quantity":       func(d bson.M) { firstDetail(d)["quantity"] = int32(0) },
		"negative price":      func(d bson.M) { firstDetail(d)["unitPriceCents"] = int64(-1) },
		"overflow":            func(d bson.M) { firstDetail(d)["unitPriceCents"] = int64(math.MaxInt64) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d := validDocument(t)
			mutate(d)
			raw, err := bson.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			if s, err := decodeSale(raw); err == nil || s != nil {
				t.Fatalf("unsafe data accepted: %v", s)
			}
		})
	}
	for _, raw := range []bson.Raw{nil, {1, 2, 3}} {
		if s, err := decodeSale(raw); err == nil || s != nil {
			t.Fatal("invalid BSON accepted")
		}
	}
	d := validDocument(t)
	duplicate := bson.D{}
	for key, value := range d {
		duplicate = append(duplicate, bson.E{Key: key, Value: value})
	}
	duplicate = append(duplicate, bson.E{Key: "active", Value: true})
	raw, err := bson.Marshal(duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSale(raw); err == nil {
		t.Fatal("duplicate BSON fields discarded")
	}
}

func TestSaleMappingRejectsInvalidInput(t *testing.T) {
	if _, err := encodeSale("fixture-key", nil); err == nil {
		t.Fatal("nil sale accepted")
	}
	s := saleFixture(t, 1)
	for _, key := range []string{"short", strings.Repeat("k", 129), "invalid\xff-key"} {
		if _, err := encodeSale(key, s); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	bad, err := model.RegisterSale("id-1", uuid.NewString(), uuid.NewString(), time.Now(), []model.LineInput{{DetailID: uuid.NewString(), ProductID: uuid.NewString(), Quantity: 1, UnitPriceCents: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeSale("fixture-key", bad); err == nil {
		t.Fatal("noncanonical UUID accepted")
	}
}

func TestMongoRepositoryRejectsBeforeInsertion(t *testing.T) {
	// Neither rejected input nor a cancelled request may reach the collection.
	repo := NewMongoRepository(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, created, err := repo.CreateOnce(ctx, "fixture-key", saleFixture(t, 1))
	if !errors.Is(err, context.Canceled) || got != nil || created {
		t.Fatalf("cancelled request reported success: %v", err)
	}
	got, created, err = repo.CreateOnce(context.Background(), "fixture-key", nil)
	if !errors.Is(err, ErrInvalidSaleDocument) || got != nil || created {
		t.Fatalf("invalid input reached insertion: %v", err)
	}
}

func TestSaleMappingDatetimeAndAmountBoundaries(t *testing.T) {
	for _, date := range []time.Time{time.Unix(0, -1), time.Time{}, time.Unix(1, 999999999)} {
		s, err := model.RegisterSale(uuid.NewString(), uuid.NewString(), uuid.NewString(), date, []model.LineInput{{DetailID: uuid.NewString(), ProductID: uuid.NewString(), Quantity: 1, UnitPriceCents: math.MaxInt64}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := encodeSale(strings.Repeat("é", 128), s)
		if err != nil {
			t.Fatal(err)
		}
		assertSaleBSON(t, raw, s, strings.Repeat("é", 128))
		got, err := decodeSale(raw)
		if err != nil {
			t.Fatal(err)
		}
		assertSale(t, got, s)
	}
}
