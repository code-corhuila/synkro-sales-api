package model

import "math"

// SaleDetail is a line of a Sale. It has no identity outside its Sale and is
// only built through the Sale's methods, so its subtotal is always computed.
type SaleDetail struct {
	id             string
	productID      string
	quantity       int
	unitPriceCents int64 // frozen by the stock reservation; never reassigned
	subtotalCents  int64
}

func (d SaleDetail) ID() string            { return d.id }
func (d SaleDetail) ProductID() string     { return d.productID }
func (d SaleDetail) Quantity() int         { return d.quantity }
func (d SaleDetail) UnitPriceCents() int64 { return d.unitPriceCents }
func (d SaleDetail) SubtotalCents() int64  { return d.subtotalCents }

func newSaleDetail(in LineInput) (SaleDetail, error) {
	if in.Quantity <= 0 {
		return SaleDetail{}, ErrInvalidQuantity
	}
	if in.Quantity > math.MaxInt32 {
		return SaleDetail{}, ErrQuantityTooLarge
	}
	if in.UnitPriceCents <= 0 {
		return SaleDetail{}, ErrInvalidUnitPrice
	}
	subtotal, err := mulExact(int64(in.Quantity), in.UnitPriceCents)
	if err != nil {
		return SaleDetail{}, err
	}
	return SaleDetail{
		id:             in.DetailID,
		productID:      in.ProductID,
		quantity:       in.Quantity,
		unitPriceCents: in.UnitPriceCents,
		subtotalCents:  subtotal,
	}, nil
}
