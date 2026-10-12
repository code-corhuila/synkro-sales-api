package in

import (
	"context"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

// ReportQuery is the validated filter shared by the three sales reports.
// From is inclusive and Until exclusive (midnight UTC bounds). CreatedBy,
// when set, restricts the report to one salesperson's sales; the inbound
// adapter derives it from the verified token, never from the request.
type ReportQuery struct {
	Page, Limit int64
	CreatedBy   string
	From, Until *time.Time
}

// ReportPage is one page of aggregated groups. Total counts all groups.
type ReportPage[T any] struct {
	Items                          []T
	Page, Limit, Total, TotalPages int64
}

type SalesReportUseCase interface {
	Daily(context.Context, ReportQuery) (ReportPage[model.DailyTotal], error)
	Monthly(context.Context, ReportQuery) (ReportPage[model.MonthlyTotal], error)
	TopProducts(context.Context, ReportQuery) (ReportPage[model.ProductSales], error)
}
