package model

import (
	"testing"
	"time"
)

func registrationSale(t *testing.T, id, customer, author string, lines ...LineInput) *Sale {
	t.Helper()
	s, err := RegisterSale(id, customer, author, time.Date(2026, 10, 11, 9, 0, 0, 0, time.UTC), lines)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSameRegistrationComparesBusinessInputOnly(t *testing.T) {
	a := LineInput{DetailID: "d1", ProductID: "p-a", Quantity: 2, UnitPriceCents: 1250}
	b := LineInput{DetailID: "d2", ProductID: "p-b", Quantity: 1, UnitPriceCents: 500}
	base := registrationSale(t, "s1", "c1", "u1", a, b)

	same := func(name string, other *Sale, want bool) {
		t.Helper()
		if got := base.SameRegistration(other); got != want {
			t.Errorf("%s: SameRegistration = %v, want %v", name, got, want)
		}
		if got := other.SameRegistration(base); got != want {
			t.Errorf("%s (reversed): SameRegistration = %v, want %v", name, got, want)
		}
	}
	other := func(mutate func(*LineInput)) LineInput { l := a; mutate(&l); return l }

	same("exact input with new generated identifiers", registrationSale(t, "s2", "c1", "u1",
		LineInput{DetailID: "x1", ProductID: "p-a", Quantity: 2, UnitPriceCents: 1250},
		LineInput{DetailID: "x2", ProductID: "p-b", Quantity: 1, UnitPriceCents: 500}), true)
	same("lines in another order", registrationSale(t, "s3", "c1", "u1", b, a), true)
	same("different instant and commercial number", func() *Sale {
		s := registrationSale(t, "s4", "c1", "u1", a, b)
		if err := s.AssignCommercialNumber("VEN-2026-000009"); err != nil {
			t.Fatal(err)
		}
		return s
	}(), true)

	same("different customer", registrationSale(t, "s5", "c2", "u1", a, b), false)
	same("different author", registrationSale(t, "s6", "c1", "u2", a, b), false)
	same("different quantity", registrationSale(t, "s7", "c1", "u1", other(func(l *LineInput) { l.Quantity = 3 }), b), false)
	same("different frozen price", registrationSale(t, "s8", "c1", "u1", other(func(l *LineInput) { l.UnitPriceCents = 9000 }), b), false)
	same("different product", registrationSale(t, "s9", "c1", "u1", other(func(l *LineInput) { l.ProductID = "p-z" }), b), false)
	same("fewer lines", registrationSale(t, "s10", "c1", "u1", a), false)
	same("extra line", registrationSale(t, "s11", "c1", "u1", a, b, LineInput{DetailID: "d3", ProductID: "p-c", Quantity: 1, UnitPriceCents: 1}), false)
	same("swapped prices between products", registrationSale(t, "s12", "c1", "u1",
		LineInput{DetailID: "d1", ProductID: "p-a", Quantity: 2, UnitPriceCents: 500},
		LineInput{DetailID: "d2", ProductID: "p-b", Quantity: 1, UnitPriceCents: 1250}), false)
}

func TestSameRegistrationRejectsNil(t *testing.T) {
	s := registrationSale(t, "s1", "c1", "u1", LineInput{DetailID: "d1", ProductID: "p", Quantity: 1, UnitPriceCents: 1})
	var none *Sale
	if s.SameRegistration(nil) || none.SameRegistration(s) || none.SameRegistration(none) {
		t.Fatal("a nil sale must never match")
	}
}

func TestSameRegistrationDoesNotMutateEitherSale(t *testing.T) {
	a := registrationSale(t, "s1", "c1", "u1",
		LineInput{DetailID: "d1", ProductID: "p-b", Quantity: 1, UnitPriceCents: 1},
		LineInput{DetailID: "d2", ProductID: "p-a", Quantity: 1, UnitPriceCents: 1})
	b := registrationSale(t, "s2", "c1", "u1",
		LineInput{DetailID: "d3", ProductID: "p-a", Quantity: 1, UnitPriceCents: 1},
		LineInput{DetailID: "d4", ProductID: "p-b", Quantity: 1, UnitPriceCents: 1})
	if !a.SameRegistration(b) {
		t.Fatal("expected equivalent registrations")
	}
	if a.Details()[0].ProductID() != "p-b" || b.Details()[0].ProductID() != "p-a" {
		t.Fatal("comparison reordered the stored lines")
	}
}
