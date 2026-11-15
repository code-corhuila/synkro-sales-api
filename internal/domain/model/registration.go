package model

import "sort"

// registrationLine is the business input of one line: what the caller asked for and the
// price frozen by the stock reservation. Generated detail identifiers and subtotals are
// outputs of the service, not input.
type registrationLine struct {
	productID      string
	quantity       int
	unitPriceCents int64
}

// SameRegistration reports whether other was requested with the same business input as s:
// customer, author and the multiset of (product, quantity, frozen unit price) lines.
// Line order is not significant, because the Workflow normalises the saga input by product
// before it registers the sale, so a retry may legitimately list the same lines in another order.
// Generated identifiers, the creation instant and the commercial number never take part.
func (s *Sale) SameRegistration(other *Sale) bool {
	if s == nil || other == nil {
		return false
	}
	if s.customerID != other.customerID || s.createdBy != other.createdBy || len(s.details) != len(other.details) {
		return false
	}
	mine, theirs := canonicalLines(s.details), canonicalLines(other.details)
	for i := range mine {
		if mine[i] != theirs[i] {
			return false
		}
	}
	return true
}

func canonicalLines(details []SaleDetail) []registrationLine {
	lines := make([]registrationLine, len(details))
	for i, d := range details {
		lines[i] = registrationLine{productID: d.productID, quantity: d.quantity, unitPriceCents: d.unitPriceCents}
	}
	sort.Slice(lines, func(i, j int) bool {
		a, b := lines[i], lines[j]
		if a.productID != b.productID {
			return a.productID < b.productID
		}
		if a.quantity != b.quantity {
			return a.quantity < b.quantity
		}
		return a.unitPriceCents < b.unitPriceCents
	})
	return lines
}
