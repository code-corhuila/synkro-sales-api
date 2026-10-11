package in

import (
	"context"
	"errors"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

var ErrInvalidIdempotencyKey = errors.New("idempotency key must contain 8 to 128 characters")

// RegisterSaleCommand is the trusted saga step 3 input. Prices are already
// frozen by stock reservation; identity, date and calculated amounts belong
// to the service. Authorization is the inbound adapter's responsibility.
type RegisterSaleCommand struct {
	IdempotencyKey string
	CustomerID     string
	CreatedBy      string
	Lines          []RegisterSaleLine
}

type RegisterSaleLine struct {
	ProductID      string
	Quantity       int
	UnitPriceCents int64
}

type RegisterSaleResult struct {
	Sale    *model.Sale
	Created bool // false when a replay returned the stored original
}

type RegisterSaleUseCase interface {
	Register(ctx context.Context, cmd RegisterSaleCommand) (RegisterSaleResult, error)
}
