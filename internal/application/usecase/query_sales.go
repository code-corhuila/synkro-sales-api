package usecase

import (
	"context"
	"errors"
	"fmt"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"math"
)

type SalesQueries struct{ repo out.SaleReadRepository }

var _ in.SalesQueryUseCase = (*SalesQueries)(nil)

func NewSalesQueries(repo out.SaleReadRepository) *SalesQueries { return &SalesQueries{repo: repo} }
func (u *SalesQueries) Get(ctx context.Context, id string) (*model.Sale, error) {
	sale, err := u.repo.FindActiveByID(ctx, id)
	if errors.Is(err, out.ErrSaleNotFound) {
		return nil, in.ErrSaleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", in.ErrPersistenceFailure, err)
	}
	if sale == nil {
		return nil, errors.New("repository returned no sale without an error")
	}
	if !sale.Active() {
		return nil, in.ErrSaleNotFound
	}
	return sale, nil
}
func (u *SalesQueries) List(ctx context.Context, q in.ListSalesQuery) (in.ListSalesResult, error) {
	if q.Page < 1 || q.Limit < 1 || q.Limit > 100 || q.Page-1 > math.MaxInt64/q.Limit || (q.From != nil && q.Until != nil && !q.From.Before(*q.Until)) {
		return in.ListSalesResult{}, in.ErrInvalidQuery
	}
	filter := out.SaleFilter{CustomerID: q.CustomerID, CreatedBy: q.CreatedBy, From: q.From, Until: q.Until, Page: q.Page, Limit: q.Limit}
	sales, err := u.repo.ListActive(ctx, filter)
	if err != nil {
		return in.ListSalesResult{}, fmt.Errorf("%w: %w", in.ErrPersistenceFailure, err)
	}
	total, err := u.repo.CountActive(ctx, filter)
	if err != nil {
		return in.ListSalesResult{}, fmt.Errorf("%w: %w", in.ErrPersistenceFailure, err)
	}
	if total < 0 {
		return in.ListSalesResult{}, errors.New("repository returned a negative count")
	}
	for _, sale := range sales {
		if sale == nil || !sale.Active() || (q.CreatedBy != "" && sale.CreatedBy() != q.CreatedBy) {
			return in.ListSalesResult{}, errors.New("repository returned a sale outside visibility scope")
		}
	}
	if sales == nil {
		sales = []*model.Sale{}
	}
	pages := total / q.Limit
	if total%q.Limit != 0 {
		pages++
	}
	return in.ListSalesResult{Sales: sales, Page: q.Page, Limit: q.Limit, Total: total, TotalPages: pages}, nil
}
