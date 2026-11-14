package model

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestNewCommercialNumberFormatsYearAndPaddedSequence(t *testing.T) {
	cases := []struct {
		year int
		seq  int64
		want string
	}{
		{2026, 1, "VEN-2026-000001"},
		{2026, 42, "VEN-2026-000042"},
		{2027, 999999, "VEN-2027-999999"},
	}
	for _, c := range cases {
		got, err := NewCommercialNumber(c.year, c.seq)
		if err != nil || got != c.want {
			t.Fatalf("NewCommercialNumber(%d,%d) = %q,%v want %q", c.year, c.seq, got, err, c.want)
		}
	}
}

func TestNewCommercialNumberRejectsOutOfRange(t *testing.T) {
	if _, err := NewCommercialNumber(2026, 0); !errors.Is(err, ErrInvalidCommercialNumber) {
		t.Fatalf("sequence 0: %v", err)
	}
	if _, err := NewCommercialNumber(2026, 1000000); !errors.Is(err, ErrCommercialSequenceExhausted) {
		t.Fatalf("overflow must be explicit, got %v", err)
	}
	if _, err := NewCommercialNumber(999, 1); !errors.Is(err, ErrInvalidCommercialNumber) {
		t.Fatalf("year 999: %v", err)
	}
}

func TestCommercialYearUsesColombiaCalendar(t *testing.T) {
	// 2027-01-01 02:00 UTC is still 2026-12-31 21:00 in Colombia (UTC-5, no DST).
	if got := CommercialYear(time.Date(2027, 1, 1, 2, 0, 0, 0, time.UTC)); got != 2026 {
		t.Fatalf("year = %d, want 2026", got)
	}
	if got := CommercialYear(time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC)); got != 2027 {
		t.Fatalf("year = %d, want 2027", got)
	}
}

func TestValidCommercialNumber(t *testing.T) {
	for _, ok := range []string{"VEN-2026-000001", "VEN-2030-999999"} {
		if !ValidCommercialNumber(ok) {
			t.Errorf("%q must be valid", ok)
		}
	}
	for _, bad := range []string{"", "VEN-000001", "ven-2026-000001", "VEN-2026-00001", "VEN-2026-0000001", "VEN-2026-000000", "VEN-26-000001"} {
		if ValidCommercialNumber(bad) {
			t.Errorf("%q must be invalid", bad)
		}
	}
}

func TestSaleAssignsCommercialNumberOnlyOnce(t *testing.T) {
	s, err := RegisterSale("11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333",
		time.Now(), []LineInput{{DetailID: "44444444-4444-4444-8444-444444444444", ProductID: "55555555-5555-4555-8555-555555555555", Quantity: 1, UnitPriceCents: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if s.CommercialNumber() != "" {
		t.Fatal("a new sale has no number until persistence assigns it")
	}
	if err := s.AssignCommercialNumber("bad"); !errors.Is(err, ErrInvalidCommercialNumber) {
		t.Fatalf("invalid format accepted: %v", err)
	}
	if err := s.AssignCommercialNumber("VEN-2026-000007"); err != nil {
		t.Fatal(err)
	}
	if err := s.AssignCommercialNumber("VEN-2026-000008"); !errors.Is(err, ErrCommercialNumberAlreadyAssigned) {
		t.Fatalf("number must be immutable once assigned: %v", err)
	}
	if s.CommercialNumber() != "VEN-2026-000007" {
		t.Fatalf("number changed: %s", s.CommercialNumber())
	}
}

func TestCommercialYearAtTheExactBoundary(t *testing.T) {
	cot := time.FixedZone("COT", -5*60*60)
	cases := []struct {
		name string
		at   time.Time
		want int
	}{
		{"last nanosecond of 2026 in Colombia", time.Date(2026, 12, 31, 23, 59, 59, 999999999, cot), 2026},
		{"first instant of 2027 in Colombia", time.Date(2027, 1, 1, 0, 0, 0, 0, cot), 2027},
		{"04:59:59.999999999 UTC is still the old year", time.Date(2027, 1, 1, 4, 59, 59, 999999999, time.UTC), 2026},
		{"05:00:00 UTC is the new year", time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC), 2027},
		{"an instant expressed in another zone", time.Date(2027, 1, 1, 10, 0, 0, 0, time.FixedZone("CET", 3600)), 2027},
		{"leap day", time.Date(2028, 2, 29, 23, 0, 0, 0, cot), 2028},
	}
	for _, c := range cases {
		if got := CommercialYear(c.at); got != c.want {
			t.Errorf("%s: year = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestEveryYearStartsAtSequenceOne(t *testing.T) {
	for year := 2026; year <= 2031; year++ {
		got, err := NewCommercialNumber(year, 1)
		want := fmt.Sprintf("VEN-%d-000001", year)
		if err != nil || got != want {
			t.Fatalf("first number of %d = %q,%v want %q", year, got, err, want)
		}
	}
}

func TestTheLastNumberOfTheYearIsIssuedAndTheNextOneFails(t *testing.T) {
	if got, err := NewCommercialNumber(2026, 999999); err != nil || got != "VEN-2026-999999" {
		t.Fatalf("999999 must still be issued: %q,%v", got, err)
	}
	if got, err := NewCommercialNumber(2026, 1000000); !errors.Is(err, ErrCommercialSequenceExhausted) || got != "" {
		t.Fatalf("1000000 must fail with no number: %q,%v", got, err)
	}
}
