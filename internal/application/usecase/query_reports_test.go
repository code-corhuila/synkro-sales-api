package usecase

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
)

type reportFake struct {
	daily   out.ReportRows[model.DailyTotal]
	monthly out.ReportRows[model.MonthlyTotal]
	top     out.ReportRows[model.ProductSales]
	err     error
	calls   []string
	filters []out.ReportFilter
}

func (f *reportFake) DailyTotals(_ context.Context, filter out.ReportFilter) (out.ReportRows[model.DailyTotal], error) {
	f.calls, f.filters = append(f.calls, "daily"), append(f.filters, filter)
	return f.daily, f.err
}
func (f *reportFake) MonthlyTotals(_ context.Context, filter out.ReportFilter) (out.ReportRows[model.MonthlyTotal], error) {
	f.calls, f.filters = append(f.calls, "monthly"), append(f.filters, filter)
	return f.monthly, f.err
}
func (f *reportFake) TopProducts(_ context.Context, filter out.ReportFilter) (out.ReportRows[model.ProductSales], error) {
	f.calls, f.filters = append(f.calls, "top"), append(f.filters, filter)
	return f.top, f.err
}

var (
	reportFrom  = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	reportUntil = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

func validReportQuery() in.ReportQuery {
	return in.ReportQuery{Page: 2, Limit: 10, CreatedBy: "seller", From: &reportFrom, Until: &reportUntil}
}

func TestDailyReportDelegatesAndPaginates(t *testing.T) {
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	f := &reportFake{daily: out.ReportRows[model.DailyTotal]{Items: []model.DailyTotal{{Day: day, TotalCents: 250000, SaleCount: 2}}, Total: 11}}
	result, err := NewSalesReports(f).Daily(context.Background(), validReportQuery())
	if err != nil {
		t.Fatal(err)
	}
	want := in.ReportPage[model.DailyTotal]{Items: f.daily.Items, Page: 2, Limit: 10, Total: 11, TotalPages: 2}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result=%+v want=%+v", result, want)
	}
	filter := out.ReportFilter{CreatedBy: "seller", From: &reportFrom, Until: &reportUntil, Page: 2, Limit: 10}
	if !reflect.DeepEqual(f.filters, []out.ReportFilter{filter}) || !reflect.DeepEqual(f.calls, []string{"daily"}) {
		t.Fatalf("delegation calls=%v filters=%+v", f.calls, f.filters)
	}
}

func TestMonthlyReportDelegatesAndPaginates(t *testing.T) {
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	f := &reportFake{monthly: out.ReportRows[model.MonthlyTotal]{Items: []model.MonthlyTotal{{Month: month, TotalCents: 900, SaleCount: 3}}, Total: 1}}
	result, err := NewSalesReports(f).Monthly(context.Background(), in.ReportQuery{Page: 1, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 1 || result.TotalPages != 1 || result.Page != 1 || result.Limit != 20 || len(result.Items) != 1 || result.Items[0].Month != month {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(f.calls, []string{"monthly"}) || f.filters[0].CreatedBy != "" || f.filters[0].From != nil || f.filters[0].Until != nil {
		t.Fatalf("unexpected delegation %v %+v", f.calls, f.filters)
	}
}

func TestTopProductsReportDelegatesAndPaginates(t *testing.T) {
	f := &reportFake{top: out.ReportRows[model.ProductSales]{Items: []model.ProductSales{{ProductID: "p", TotalQuantity: 7, TotalRevenueCents: 700}}, Total: 41}}
	result, err := NewSalesReports(f).TopProducts(context.Background(), in.ReportQuery{Page: 1, Limit: 20, CreatedBy: "seller"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 41 || result.TotalPages != 3 || len(result.Items) != 1 || result.Items[0].TotalRevenueCents != 700 {
		t.Fatalf("result=%+v", result)
	}
	if !reflect.DeepEqual(f.calls, []string{"top"}) || f.filters[0].CreatedBy != "seller" {
		t.Fatalf("unexpected delegation %v %+v", f.calls, f.filters)
	}
}

func TestReportsReturnEmptyNonNilPageWithoutGroups(t *testing.T) {
	u := NewSalesReports(&reportFake{})
	q := in.ReportQuery{Page: 1, Limit: 20}
	daily, err := u.Daily(context.Background(), q)
	if err != nil || daily.Items == nil || len(daily.Items) != 0 || daily.Total != 0 || daily.TotalPages != 0 {
		t.Fatalf("daily=%+v err=%v", daily, err)
	}
	monthly, err := u.Monthly(context.Background(), q)
	if err != nil || monthly.Items == nil || monthly.TotalPages != 0 {
		t.Fatalf("monthly=%+v err=%v", monthly, err)
	}
	top, err := u.TopProducts(context.Background(), q)
	if err != nil || top.Items == nil || top.TotalPages != 0 {
		t.Fatalf("top=%+v err=%v", top, err)
	}
}

func TestReportsRejectInvalidQueryBeforeRepository(t *testing.T) {
	later, earlier := reportUntil, reportFrom
	cases := map[string]in.ReportQuery{
		"page zero":       {Page: 0, Limit: 20},
		"page negative":   {Page: -1, Limit: 20},
		"limit zero":      {Page: 1, Limit: 0},
		"limit above 100": {Page: 1, Limit: 101},
		"offset overflow": {Page: math.MaxInt64, Limit: 100},
		"reversed range":  {Page: 1, Limit: 20, From: &later, Until: &earlier},
		"empty range":     {Page: 1, Limit: 20, From: &earlier, Until: &earlier},
	}
	for name, q := range cases {
		t.Run(name, func(t *testing.T) {
			f := &reportFake{}
			u := NewSalesReports(f)
			if _, err := u.Daily(context.Background(), q); !errors.Is(err, in.ErrInvalidQuery) {
				t.Fatalf("daily err=%v", err)
			}
			if _, err := u.Monthly(context.Background(), q); !errors.Is(err, in.ErrInvalidQuery) {
				t.Fatalf("monthly err=%v", err)
			}
			if _, err := u.TopProducts(context.Background(), q); !errors.Is(err, in.ErrInvalidQuery) {
				t.Fatalf("top err=%v", err)
			}
			if len(f.calls) != 0 {
				t.Fatal("invalid query reached the repository")
			}
		})
	}
}

func TestReportsWrapRepositoryFailures(t *testing.T) {
	cause := errors.New("driver exploded")
	f := &reportFake{err: cause}
	u := NewSalesReports(f)
	q := in.ReportQuery{Page: 1, Limit: 20}
	_, e1 := u.Daily(context.Background(), q)
	_, e2 := u.Monthly(context.Background(), q)
	_, e3 := u.TopProducts(context.Background(), q)
	for _, err := range []error{e1, e2, e3} {
		if !errors.Is(err, in.ErrPersistenceFailure) || !errors.Is(err, cause) {
			t.Fatalf("failure not propagated as persistence failure: %v", err)
		}
	}
}

func TestReportsRejectInconsistentRepositoryResults(t *testing.T) {
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	good := model.DailyTotal{Day: day, TotalCents: 1, SaleCount: 1}
	cases := map[string]out.ReportRows[model.DailyTotal]{
		"negative total":        {Total: -1},
		"more items than limit": {Items: []model.DailyTotal{good, good, good}, Total: 3},
		"items exceed total":    {Items: []model.DailyTotal{good, good}, Total: 1},
		"negative amount":       {Items: []model.DailyTotal{{Day: day, TotalCents: -1, SaleCount: 1}}, Total: 1},
		"negative count":        {Items: []model.DailyTotal{{Day: day, TotalCents: 1, SaleCount: -1}}, Total: 1},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewSalesReports(&reportFake{daily: rows}).Daily(context.Background(), in.ReportQuery{Page: 1, Limit: 2})
			if err == nil || errors.Is(err, in.ErrInvalidQuery) {
				t.Fatalf("inconsistent result accepted or misclassified: %v", err)
			}
		})
	}
	_, err := NewSalesReports(&reportFake{monthly: out.ReportRows[model.MonthlyTotal]{Items: []model.MonthlyTotal{{TotalCents: -5}}, Total: 1}}).Monthly(context.Background(), in.ReportQuery{Page: 1, Limit: 2})
	if err == nil {
		t.Fatal("negative monthly amount accepted")
	}
	_, err = NewSalesReports(&reportFake{top: out.ReportRows[model.ProductSales]{Items: []model.ProductSales{{ProductID: "p", TotalQuantity: 0}}, Total: 1}}).TopProducts(context.Background(), in.ReportQuery{Page: 1, Limit: 2})
	if err == nil {
		t.Fatal("zero-quantity product group accepted")
	}
}

func TestReportsPropagateContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &reportFake{err: ctx.Err()}
	_, err := NewSalesReports(f).Daily(ctx, in.ReportQuery{Page: 1, Limit: 20})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
