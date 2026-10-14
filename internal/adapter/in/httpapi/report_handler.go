package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

const reportsPath = "/api/v1/sales/reports"

// serveReport dispatches the exact report routes. Anything else below the
// reserved reports namespace is an unknown resource, never a sale identifier.
func (d Dependencies) serveReport(w http.ResponseWriter, r *http.Request) {
	var handle func(http.ResponseWriter, *http.Request)
	switch r.URL.Path {
	case reportsPath + "/daily":
		handle = d.dailyReport
	case reportsPath + "/monthly":
		handle = d.monthlyReport
	case reportsPath + "/top-products":
		handle = d.topProductsReport
	default:
		writeError(w, 404, "NOT_FOUND", "Resource not found", nil)
		return
	}
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		validationError(w, "method", "unsupported method")
		return
	}
	handle(w, r)
}

func (d Dependencies) dailyReport(w http.ResponseWriter, r *http.Request) {
	respond(d, w, r, func(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.DailyTotal], error) {
		return d.Reports.Daily(ctx, q)
	}, func(t model.DailyTotal) DailyTotalResponse {
		return DailyTotalResponse{Day: t.Day.UTC().Format(time.DateOnly), DailyTotalCents: t.TotalCents, SaleCount: t.SaleCount}
	})
}

func (d Dependencies) monthlyReport(w http.ResponseWriter, r *http.Request) {
	respond(d, w, r, func(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.MonthlyTotal], error) {
		return d.Reports.Monthly(ctx, q)
	}, func(t model.MonthlyTotal) MonthlyTotalResponse {
		return MonthlyTotalResponse{Month: t.Month.UTC().Format(time.DateOnly), MonthlyTotalCents: t.TotalCents, SaleCount: t.SaleCount}
	})
}

func (d Dependencies) topProductsReport(w http.ResponseWriter, r *http.Request) {
	respond(d, w, r, func(ctx context.Context, q in.ReportQuery) (in.ReportPage[model.ProductSales], error) {
		return d.Reports.TopProducts(ctx, q)
	}, func(p model.ProductSales) TopProductResponse {
		return TopProductResponse{ProductID: p.ProductID, TotalQuantity: p.TotalQuantity, TotalRevenueCents: p.TotalRevenueCents}
	})
}

// respond authorizes, validates, scopes a SALESPERSON to the token's own
// sales, runs one report and writes the paginated envelope. The order is
// authorization, then validation, then I/O.
func respond[T, R any](d Dependencies, w http.ResponseWriter, r *http.Request, run func(context.Context, in.ReportQuery) (in.ReportPage[T], error), row func(T) R) {
	p := principalFrom(r.Context())
	if !authorize(w, p.CanReadReports()) {
		return
	}
	q, ok := reportQuery(w, r)
	if !ok {
		return
	}
	if p.Role == "SALESPERSON" {
		q.CreatedBy = p.Subject
	}
	if d.Reports == nil {
		d.failure(w, r, errors.New("report dependency missing"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), d.Timeout)
	defer cancel()
	page, err := run(ctx, q)
	if err != nil {
		d.failure(w, r, err)
		return
	}
	response := ReportResponse[R]{Data: make([]R, 0, len(page.Items)), Meta: PageMeta{Page: page.Page, Limit: page.Limit, Total: page.Total, TotalPages: page.TotalPages}}
	for _, item := range page.Items {
		response.Data = append(response.Data, row(item))
	}
	writeJSON(w, 200, response)
}
