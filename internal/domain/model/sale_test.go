package model

import (
	"fmt"
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
			if !errorsIs(err, tt.want) {
				t.Errorf("got %v, want %v", err, tt.want)
			}
		})
	}
}
