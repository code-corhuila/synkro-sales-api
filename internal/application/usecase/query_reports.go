package usecase

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

type SalesReports struct{ repo out.SalesReportRepository }

var _ in.SalesReportUseCase = (*SalesReports)(nil)

func NewSalesReports(repo out.SalesReportRepository) *SalesReports {
	return &SalesReports{repo: repo}
}

func (u *SalesReports) Daily(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.DailyTotal], error) {
	return report(ctx, q, u.repo.DailyTotals, func(d model.DailyTotal) bool { return d.TotalCents >= 0 && d.SaleCount >= 0 })
}

func (u *SalesReports) Monthly(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.MonthlyTotal], error) {
	return report(ctx, q, u.repo.MonthlyTotals, func(m model.MonthlyTotal) bool { return m.TotalCents >= 0 && m.SaleCount >= 0 })
}

func (u *SalesReports) TopProducts(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.ProductSales], error) {
	return report(ctx, q, u.repo.TopProducts, func(p model.ProductSales) bool {
		return p.ProductID != "" && p.TotalQuantity > 0 && p.TotalRevenueCents >= 0
	})
}

// report validates the query before any I/O, delegates one aggregation and
// verifies the adapter honored the paging contract.
func report[T any](ctx context.Context, q in.ReportQuery, fetch func(context.Context, out.ReportFilter) (out.ReportRows[T], error), plausible func(T) bool) (in.ReportPage[T], error) {
	if q.Page < 1 || q.Limit < 1 || q.Limit > 100 || q.Page-1 > math.MaxInt64/q.Limit || (q.From != nil && q.Until != nil && !q.From.Before(*q.Until)) {
		return in.ReportPage[T]{}, in.ErrInvalidQuery
	}
	rows, err := fetch(ctx, out.ReportFilter{CreatedBy: q.CreatedBy, From: q.From, Until: q.Until, Page: q.Page, Limit: q.Limit})
	if err != nil {
		return in.ReportPage[T]{}, fmt.Errorf("%w: %w", in.ErrPersistenceFailure, err)
	}
	if rows.Total < 0 || int64(len(rows.Items)) > q.Limit || int64(len(rows.Items)) > rows.Total {
		return in.ReportPage[T]{}, errors.New("repository returned an inconsistent report page")
	}
	for _, item := range rows.Items {
		if !plausible(item) {
			return in.ReportPage[T]{}, errors.New("repository returned an invalid report group")
		}
	}
	items := rows.Items
	if items == nil {
		items = []T{}
	}
	pages := rows.Total / q.Limit
	if rows.Total%q.Limit != 0 {
		pages++
	}
	return in.ReportPage[T]{Items: items, Page: q.Page, Limit: q.Limit, Total: rows.Total, TotalPages: pages}, nil
}
