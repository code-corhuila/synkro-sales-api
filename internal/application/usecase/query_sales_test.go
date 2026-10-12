package usecase

import (
	"context"
	"errors"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"reflect"
	"testing"
	"time"
)

type readFake struct {
	sale                       *model.Sale
	rows                       []*model.Sale
	total                      int64
	findErr, listErr, countErr error
	filters                    []out.SaleFilter
}

func (f *readFake) FindActiveByID(context.Context, string) (*model.Sale, error) {
	return f.sale, f.findErr
}
func (f *readFake) ListActive(_ context.Context, filter out.SaleFilter) ([]*model.Sale, error) {
	f.filters = append(f.filters, filter)
	return f.rows, f.listErr
}
func (f *readFake) CountActive(_ context.Context, filter out.SaleFilter) (int64, error) {
	f.filters = append(f.filters, filter)
	return f.total, f.countErr
}

func TestSalesQueries(t *testing.T) {
	s, err := model.RegisterSale("sale", "customer", "creator", time.Now(), []model.LineInput{{Quantity: 1, UnitPriceCents: 120}})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 0, 1)
	f := &readFake{sale: s, rows: []*model.Sale{s}, total: 21}
	u := NewSalesQueries(f)
	q := in.ListSalesQuery{Page: 2, Limit: 20, CreatedBy: "creator", CustomerID: "customer", From: &start, Until: &end}
	result, err := u.List(context.Background(), q)
	if err != nil || result.Total != 21 || result.TotalPages != 2 || len(result.Sales) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(f.filters) != 2 || !reflect.DeepEqual(f.filters[0], f.filters[1]) || f.filters[0].CreatedBy != "creator" || f.filters[0].CustomerID != "customer" || f.filters[0].Page != 2 || f.filters[0].Limit != 20 || !f.filters[0].From.Equal(start) || !f.filters[0].Until.Equal(end) {
		t.Fatalf("list/count visibility mismatch: %+v", f.filters)
	}
	got, err := u.Get(context.Background(), s.ID())
	if err != nil || got != s {
		t.Fatal("detail failed", err)
	}
	f.rows = nil
	f.total = 0
	result, err = u.List(context.Background(), q)
	if err != nil || result.Sales == nil || result.TotalPages != 0 {
		t.Fatal("empty pagination failed", err)
	}
	for _, q := range []in.ListSalesQuery{{Page: 0, Limit: 20}, {Page: 1, Limit: 0}, {Page: 1, Limit: 101}, {Page: 1, Limit: 20, From: &end, Until: &start}, {Page: 9223372036854775807, Limit: 100}} {
		if _, err := u.List(context.Background(), q); !errors.Is(err, in.ErrInvalidQuery) {
			t.Fatal("invalid query passed", err)
		}
	}
	boom := errors.New("database failed")
	f.listErr = boom
	if _, err := u.List(context.Background(), in.ListSalesQuery{Page: 1, Limit: 20}); !errors.Is(err, boom) {
		t.Fatal("list error lost")
	}
	f.listErr = nil
	f.countErr = boom
	if _, err := u.List(context.Background(), in.ListSalesQuery{Page: 1, Limit: 20}); !errors.Is(err, boom) {
		t.Fatal("count error lost")
	}
	f.findErr = boom
	if _, err := u.Get(context.Background(), "id"); !errors.Is(err, boom) {
		t.Fatal("detail error lost")
	}
	f.findErr = out.ErrSaleNotFound
	if _, err := u.Get(context.Background(), "id"); !errors.Is(err, in.ErrSaleNotFound) {
		t.Fatal("missing sale not mapped")
	}
	f.findErr = nil
	f.sale = nil
	if _, err := u.Get(context.Background(), "id"); err == nil {
		t.Fatal("nil success accepted")
	}
}
