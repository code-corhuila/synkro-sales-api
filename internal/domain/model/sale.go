package model

import (
	"errors"
	"time"
)

const maxSaleLines = 100

var (
	ErrNoLines      = errors.New("a sale needs at least one line")
	ErrTooManyLines = errors.New("a sale has at most 100 lines")

	ErrInvalidQuantity  = errors.New("quantity must be greater than 0")
	ErrInvalidUnitPrice = errors.New("unit price must be greater than 0")
	ErrQuantityTooLarge = errors.New("quantity exceeds the int32 range of the stored document")
)

// LineInput is what the caller supplies for one line. Subtotals and the
// total are never accepted from the caller.
type LineInput struct {
	DetailID       string
	ProductID      string
	Quantity       int
	UnitPriceCents int64
}

// Sale is the aggregate root: its lines are reached and changed only
// through it.
type Sale struct {
	id         string
	customerID string
	createdBy  string
	date       time.Time
	totalCents int64
	active     bool
	details    []SaleDetail
}

func (s *Sale) ID() string         { return s.id }
func (s *Sale) CustomerID() string { return s.customerID }
func (s *Sale) CreatedBy() string  { return s.createdBy }
func (s *Sale) Date() time.Time    { return s.date }
func (s *Sale) TotalCents() int64  { return s.totalCents }
func (s *Sale) Active() bool       { return s.active }

// Details returns a copy, so callers cannot alter the aggregate's lines.
func (s *Sale) Details() []SaleDetail { return append([]SaleDetail(nil), s.details...) }

// RegisterSale creates an active sale with its lines and calculates the total.
// Identifiers and the date come from the caller, as with NewProduct.
func RegisterSale(id, customerID, createdBy string, date time.Time, lines []LineInput) (*Sale, error) {
	if len(lines) == 0 {
		return nil, ErrNoLines
	}
	if len(lines) > maxSaleLines {
		return nil, ErrTooManyLines
	}
	details := make([]SaleDetail, 0, len(lines))
	for _, in := range lines {
		d, err := newSaleDetail(in)
		if err != nil {
			return nil, err
		}
		details = append(details, d)
	}
	s := &Sale{id: id, customerID: customerID, createdBy: createdBy, date: date, active: true, details: details}
	if _, err := s.CalculateTotal(); err != nil {
		return nil, err
	}
	return s, nil
}

func sumSubtotals(details []SaleDetail) (int64, error) {
	var total int64
	var err error
	for _, d := range details {
		if total, err = addExact(total, d.subtotalCents); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// AddDetail appends a line and recalculates the total. On error the sale is
// left unchanged.
func (s *Sale) AddDetail(detailID, productID string, quantity int, unitPriceCents int64) error {
	if len(s.details) >= maxSaleLines {
		return ErrTooManyLines
	}
	d, err := newSaleDetail(LineInput{DetailID: detailID, ProductID: productID, Quantity: quantity, UnitPriceCents: unitPriceCents})
	if err != nil {
		return err
	}
	s.details = append(s.details, d)
	if _, err := s.CalculateTotal(); err != nil {
		s.details = s.details[:len(s.details)-1]
		return err
	}
	return nil
}

// CalculateTotal recomputes the total from the current lines, stores it and
// returns it. Calling it again without changing the lines gives the same value.
func (s *Sale) CalculateTotal() (int64, error) {
	total, err := sumSubtotals(s.details)
	if err != nil {
		return 0, err
	}
	s.totalCents = total
	return total, nil
}
