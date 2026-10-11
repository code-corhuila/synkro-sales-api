package model

import (
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

var saleDate = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func lineInput(n int, quantity int, unitPriceCents int64) LineInput {
	return LineInput{
		DetailID:       fmt.Sprintf("detail-%d", n),
		ProductID:      fmt.Sprintf("product-%d", n),
		Quantity:       quantity,
		UnitPriceCents: unitPriceCents,
	}
}

func lines(count int) []LineInput {
	out := make([]LineInput, count)
	for i := range out {
		out[i] = lineInput(i, i+1, int64(1000+i))
	}
	return out
}

func TestRegisterSale_Succeeds(t *testing.T) {
	for _, count := range []int{1, 100} {
		t.Run(fmt.Sprintf("%d lines", count), func(t *testing.T) {
			sale, err := RegisterSale("sale-1", "customer-1", "user-1", saleDate, lines(count))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !sale.Active() {
				t.Error("a new sale must be active")
			}
			if sale.ID() != "sale-1" || sale.CustomerID() != "customer-1" || sale.CreatedBy() != "user-1" || !sale.Date().Equal(saleDate) {
				t.Errorf("header fields not stored: %+v", sale)
			}
			details := sale.Details()
			if len(details) != count {
				t.Fatalf("got %d details, want %d", len(details), count)
			}
			var sum int64
			for _, d := range details {
				if want := int64(d.Quantity()) * d.UnitPriceCents(); d.SubtotalCents() != want {
					t.Errorf("subtotal = %d, want quantity*unitPrice = %d", d.SubtotalCents(), want)
				}
				if d.ID() == "" || d.ProductID() == "" {
					t.Error("line identifiers not stored")
				}
				sum += d.SubtotalCents()
			}
			if sale.TotalCents() != sum {
				t.Errorf("total = %d, want sum of subtotals %d", sale.TotalCents(), sum)
			}
		})
	}
}

func TestRegisterSale_LineCountBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		count int
		want  error
	}{
		{"zero lines", 0, ErrNoLines},
		{"101 lines", 101, ErrTooManyLines},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RegisterSale("sale-1", "customer-1", "user-1", saleDate, lines(tt.count))
			if !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestRegisterSale_RejectsInvalidLineFields(t *testing.T) {
	tests := []struct {
		name string
		line LineInput
		want error
	}{
		{"zero quantity", lineInput(0, 0, 1000), ErrInvalidQuantity},
		{"negative quantity", lineInput(0, -1, 1000), ErrInvalidQuantity},
		{"zero unit price", lineInput(0, 1, 0), ErrInvalidUnitPrice},
		{"negative unit price", lineInput(0, 1, -5), ErrInvalidUnitPrice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := append(lines(2), tt.line)
			sale, err := RegisterSale("sale-1", "customer-1", "user-1", saleDate, in)
			if !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if sale != nil {
				t.Error("no sale must be returned on error")
			}
		})
	}
}

func newTestSale(t *testing.T, count int) *Sale {
	t.Helper()
	sale, err := RegisterSale("sale-1", "customer-1", "user-1", saleDate, lines(count))
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	return sale
}

func TestSale_AddDetail_RecalculatesTotal(t *testing.T) {
	sale := newTestSale(t, 2)
	before := sale.TotalCents()

	if err := sale.AddDetail("detail-new", "product-new", 3, 2500); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got, want := sale.TotalCents(), before+3*2500; got != want {
		t.Errorf("total = %d, want %d", got, want)
	}
	details := sale.Details()
	if len(details) != 3 || details[2].SubtotalCents() != 7500 {
		t.Errorf("new line not appended correctly: %+v", details)
	}
}

func TestSale_AddDetail_RejectsInvalidLinesWithoutChangingTheSale(t *testing.T) {
	tests := []struct {
		name     string
		quantity int
		price    int64
		want     error
	}{
		{"zero quantity", 0, 100, ErrInvalidQuantity},
		{"zero unit price", 1, 0, ErrInvalidUnitPrice},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sale := newTestSale(t, 2)
			before := sale.TotalCents()
			if err := sale.AddDetail("d", "p", tt.quantity, tt.price); !errors.Is(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
			if sale.TotalCents() != before || len(sale.Details()) != 2 {
				t.Error("a failed AddDetail must leave the sale unchanged")
			}
		})
	}
}

func TestSale_AddDetail_RespectsTheLineLimit(t *testing.T) {
	sale := newTestSale(t, 100)
	before := sale.TotalCents()
	if err := sale.AddDetail("d", "p", 1, 100); !errors.Is(err, ErrTooManyLines) {
		t.Errorf("got %v, want ErrTooManyLines", err)
	}
	if sale.TotalCents() != before || len(sale.Details()) != 100 {
		t.Error("a failed AddDetail must leave the sale unchanged")
	}
}

func TestSale_CalculateTotal_IsIdempotent(t *testing.T) {
	sale := newTestSale(t, 5)
	first, err := sale.CalculateTotal()
	if err != nil {
		t.Fatal(err)
	}
	second, err := sale.CalculateTotal()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || second != sale.TotalCents() {
		t.Errorf("totals differ: %d, %d, stored %d", first, second, sale.TotalCents())
	}
}

func TestSale_UnitPriceIsFrozen(t *testing.T) {
	sale := newTestSale(t, 3)
	frozen := make([]int64, 0, 3)
	for _, d := range sale.Details() {
		frozen = append(frozen, d.UnitPriceCents())
	}

	_ = sale.AddDetail("d", "p", 2, 999)
	_, _ = sale.CalculateTotal()
	// Mutating the returned copy must not reach the aggregate.
	copied := sale.Details()
	copied[0].unitPriceCents = 1

	for i, d := range sale.Details()[:3] {
		if d.UnitPriceCents() != frozen[i] {
			t.Errorf("line %d unit price changed from %d to %d", i, frozen[i], d.UnitPriceCents())
		}
	}
}

func TestRegisterSale_SubtotalOverflow(t *testing.T) {
	tests := []struct {
		name     string
		quantity int
		price    int64
		wantErr  error
		subtotal int64
	}{
		{"max price, quantity 1 fits", 1, math.MaxInt64, nil, math.MaxInt64},
		{"largest even product fits", 2, math.MaxInt64 / 2, nil, math.MaxInt64 - 1},
		{"one cent over: 2 x (MaxInt64/2+1)", 2, math.MaxInt64/2 + 1, ErrAmountOverflow, 0},
		{"MaxInt32 x (MaxInt64/MaxInt32) fits", math.MaxInt32, math.MaxInt64 / math.MaxInt32, nil, math.MaxInt32 * (math.MaxInt64 / math.MaxInt32)},
		{"MaxInt32 x (MaxInt64/MaxInt32 + 1) overflows", math.MaxInt32, math.MaxInt64/math.MaxInt32 + 1, ErrAmountOverflow, 0},
		{"MaxInt32 x MaxInt64 overflows", math.MaxInt32, math.MaxInt64, ErrAmountOverflow, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sale, err := RegisterSale("s", "c", "u", saleDate, []LineInput{lineInput(0, tt.quantity, tt.price)})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && sale.Details()[0].SubtotalCents() != tt.subtotal {
				t.Errorf("subtotal = %d, want %d", sale.Details()[0].SubtotalCents(), tt.subtotal)
			}
		})
	}
}

func TestRegisterSale_TotalOverflow(t *testing.T) {
	tests := []struct {
		name    string
		second  int64
		wantErr error
	}{
		{"MaxInt64-1 + 1 equals MaxInt64 and fits", 1, nil},
		{"MaxInt64-1 + 2 overflows", 2, ErrAmountOverflow},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := []LineInput{lineInput(0, 1, math.MaxInt64-1), lineInput(1, 1, tt.second)}
			sale, err := RegisterSale("s", "c", "u", saleDate, in)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("got %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && sale.TotalCents() != math.MaxInt64 {
				t.Errorf("total = %d, want MaxInt64", sale.TotalCents())
			}
		})
	}
}

func TestSale_AddDetail_OverflowLeavesSaleUnchanged(t *testing.T) {
	sale, err := RegisterSale("s", "c", "u", saleDate, []LineInput{lineInput(0, 1, math.MaxInt64-1)})
	if err != nil {
		t.Fatal(err)
	}
	if err := sale.AddDetail("d", "p", 1, 2); !errors.Is(err, ErrAmountOverflow) {
		t.Fatalf("got %v, want ErrAmountOverflow", err)
	}
	if sale.TotalCents() != math.MaxInt64-1 || len(sale.Details()) != 1 {
		t.Errorf("sale changed after rejected line: total=%d lines=%d", sale.TotalCents(), len(sale.Details()))
	}
	if err := sale.AddDetail("d", "p", 1, 1); err != nil || sale.TotalCents() != math.MaxInt64 {
		t.Errorf("adding exactly up to MaxInt64 must work: err=%v total=%d", err, sale.TotalCents())
	}
}

func TestSaleDetail_QuantityInt32Range(t *testing.T) {
	tests := []struct {
		name     string
		quantity int
		wantErr  error
	}{
		{"MaxInt32 is accepted", math.MaxInt32, nil},
		{"MaxInt32+1 is rejected", math.MaxInt32 + 1, ErrQuantityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := RegisterSale("s", "c", "u", saleDate, []LineInput{lineInput(0, tt.quantity, 1)})
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("RegisterSale: got %v, want %v", err, tt.wantErr)
			}
			sale := newTestSale(t, 1)
			if err := sale.AddDetail("d", "p", tt.quantity, 1); !errors.Is(err, tt.wantErr) {
				t.Errorf("AddDetail: got %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestMulExact_FullInt64Operands(t *testing.T) {
	if got, err := mulExact(math.MaxInt64/3, 3); err != nil || got != (math.MaxInt64/3)*3 {
		t.Errorf("MaxInt64/3 x 3: got %d, %v", got, err)
	}
	if _, err := mulExact(math.MaxInt64/3+1, 3); !errors.Is(err, ErrAmountOverflow) {
		t.Errorf("MaxInt64/3+1 x 3: got %v, want ErrAmountOverflow", err)
	}
	if _, err := mulExact(math.MaxInt64, math.MaxInt64); !errors.Is(err, ErrAmountOverflow) {
		t.Errorf("MaxInt64 x MaxInt64: got %v, want ErrAmountOverflow", err)
	}
}
