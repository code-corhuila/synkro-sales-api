package out

import (
	"context"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

// ReportFilter selects the sales that contribute to a report. From is
// inclusive and Until exclusive. Page and Limit apply to the aggregated
// groups, after grouping and ordering.
type ReportFilter struct {
	CreatedBy   string
	From, Until *time.Time
	Page, Limit int64
}

// ReportRows holds one page of groups plus the count of all groups matching
// the filter, both taken from the same aggregation snapshot.
type ReportRows[T any] struct {
	Items []T
	Total int64
}

// SalesReportRepository aggregates active sales inside the datastore. Every
// method excludes inactive sales, applies the ownership filter before
// grouping and orders deterministically: days and months newest first,
// products by quantity descending then product ID ascending.
type SalesReportRepository interface {
	DailyTotals(context.Context, ReportFilter) (ReportRows[model.DailyTotal], error)
	MonthlyTotals(context.Context, ReportFilter) (ReportRows[model.MonthlyTotal], error)
	TopProducts(context.Context, ReportFilter) (ReportRows[model.ProductSales], error)
}
