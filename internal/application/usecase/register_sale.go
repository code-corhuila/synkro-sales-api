package usecase

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

type RegisterSale struct {
	repo  out.SaleRepository
	ids   out.IDGenerator
	clock out.Clock
}

var _ in.RegisterSaleUseCase = (*RegisterSale)(nil)

// NewRegisterSale injects the persistence, identity and time providers.
// Providers must support concurrent calls when this use case is shared.
func NewRegisterSale(repo out.SaleRepository, ids out.IDGenerator, clock out.Clock) *RegisterSale {
	return &RegisterSale{repo: repo, ids: ids, clock: clock}
}

func (u *RegisterSale) Register(ctx context.Context, cmd in.RegisterSaleCommand) (in.RegisterSaleResult, error) {
	keyLength := utf8.RuneCountInString(cmd.IdempotencyKey)
	if keyLength < 8 || keyLength > 128 {
		return in.RegisterSaleResult{}, in.ErrInvalidIdempotencyKey
	}

	saleID := u.ids.NewID()
	lines := make([]model.LineInput, len(cmd.Lines))
	for i, line := range cmd.Lines {
		lines[i] = model.LineInput{
			DetailID: u.ids.NewID(), ProductID: line.ProductID,
			Quantity: line.Quantity, UnitPriceCents: line.UnitPriceCents,
		}
	}
	sale, err := model.RegisterSale(saleID, cmd.CustomerID, cmd.CreatedBy, u.clock.Now().UTC(), lines)
	if err != nil {
		return in.RegisterSaleResult{}, err
	}

	stored, created, err := u.repo.CreateOnce(ctx, cmd.IdempotencyKey, sale)
	if err != nil {
		return in.RegisterSaleResult{}, fmt.Errorf("%w: %w", in.ErrPersistenceFailure, err)
	}
	return in.RegisterSaleResult{Sale: stored, Created: created}, nil
}
