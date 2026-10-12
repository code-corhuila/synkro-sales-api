package persistence

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// reportLine is one stored line; inactive lines stay in the document.
type reportLine struct {
	product  string
	quantity int
	price    int64
	inactive bool
}

// reportProduct returns a fixed, ordered UUID so tie-breaking is assertable.
func reportProduct(n int) string { return fmt.Sprintf("aaaaaaaa-0000-4000-8000-%012d", n) }

func utc(year int, month time.Month, day, hour, minute, second, millisecond int) time.Time {
	return time.Date(year, month, day, hour, minute, second, millisecond*int(time.Millisecond), time.UTC)
}

type groupTotal struct {
	at           time.Time
	total, count int64
}

// setDetailField rewrites one field of a decoded detail (bson.D in an array).
func setDetailField(doc bson.M, index int, key string, value any) {
	detail := doc["details"].(bson.A)[index].(bson.D)
	for i := range detail {
		if detail[i].Key == key {
			detail[i].Value = value
		}
	}
}

// runReportIntegration exercises the real aggregation pipelines against the
// disposable MongoDB 7.0 replica set. Every scenario owns a distinct UTC year
// (or a unique owner) so the shared collection cannot disturb it.
func runReportIntegration(t *testing.T, client *mongo.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	collection := client.Database("sales").Collection("sale")
	repo := NewMongoRepository(collection)
	filter := func(from, until time.Time, owner string, pageNo, limit int64) out.ReportFilter {
		return out.ReportFilter{CreatedBy: owner, From: &from, Until: &until, Page: pageNo, Limit: limit}
	}
	year := func(y int) (time.Time, time.Time) { return utc(y, 1, 1, 0, 0, 0, 0), utc(y+1, 1, 1, 0, 0, 0, 0) }
	store := func(t *testing.T, sale *model.Sale, mutate func(bson.M)) {
		t.Helper()
		raw, err := encodeSale(uuid.NewString()+":report", sale)
		if err != nil {
			t.Fatal(err)
		}
		var doc bson.M
		if err := bson.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		mutate(doc)
		if _, err := collection.InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(t *testing.T, owner string, date time.Time, active bool, lines ...reportLine) {
		t.Helper()
		inputs := make([]model.LineInput, len(lines))
		for i, l := range lines {
			inputs[i] = model.LineInput{DetailID: uuid.NewString(), ProductID: l.product, Quantity: l.quantity, UnitPriceCents: l.price}
		}
		sale, err := model.RegisterSale(uuid.NewString(), uuid.NewString(), owner, date, inputs)
		if err != nil {
			t.Fatal(err)
		}
		store(t, sale, func(doc bson.M) {
			doc["active"] = active
			for i, l := range lines {
				setDetailField(doc, i, "active", !l.inactive)
			}
		})
	}
	// insertAmounts stores a schema-valid sale whose amounts are overridden,
	// as the migrated validator checks only types and ranges (ADR-010).
	insertAmounts := func(t *testing.T, owner string, date time.Time, total, subtotal int64) {
		t.Helper()
		sale, err := model.RegisterSale(uuid.NewString(), uuid.NewString(), owner, date, []model.LineInput{{DetailID: uuid.NewString(), ProductID: reportProduct(99), Quantity: 1, UnitPriceCents: 1}})
		if err != nil {
			t.Fatal(err)
		}
		store(t, sale, func(doc bson.M) {
			doc["totalCents"] = total
			setDetailField(doc, 0, "subtotalCents", subtotal)
		})
	}
	daily := func(t *testing.T, f out.ReportFilter) ([]groupTotal, int64) {
		t.Helper()
		rows, err := repo.DailyTotals(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		got := []groupTotal{}
		for _, r := range rows.Items {
			got = append(got, groupTotal{r.Day, r.TotalCents, r.SaleCount})
		}
		return got, rows.Total
	}
	monthly := func(t *testing.T, f out.ReportFilter) ([]groupTotal, int64) {
		t.Helper()
		rows, err := repo.MonthlyTotals(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		got := []groupTotal{}
		for _, r := range rows.Items {
			got = append(got, groupTotal{r.Month, r.TotalCents, r.SaleCount})
		}
		return got, rows.Total
	}
	same := func(t *testing.T, got, want []groupTotal) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("got=%+v want=%+v", got, want)
		}
		for i := range want {
			if !got[i].at.Equal(want[i].at) || got[i].at.Location() != time.UTC || got[i].total != want[i].total || got[i].count != want[i].count {
				t.Fatalf("group %d: got=%+v want=%+v", i, got, want)
			}
		}
	}

	t.Run("ActiveSelectionAndGrouping", func(t *testing.T) {
		from, until := year(2031)
		a, b := uuid.NewString(), uuid.NewString()
		insert(t, a, utc(2031, 3, 10, 0, 0, 0, 0), true, reportLine{product: reportProduct(1), quantity: 1, price: 1000})
		insert(t, b, utc(2031, 3, 10, 23, 59, 59, 999), true, reportLine{product: reportProduct(1), quantity: 2, price: 1250})
		insert(t, a, utc(2031, 3, 11, 0, 0, 0, 0), true, reportLine{product: reportProduct(2), quantity: 1, price: 300})
		insert(t, a, utc(2031, 3, 10, 12, 0, 0, 0), false, reportLine{product: reportProduct(3), quantity: 1, price: 777777})
		got, total := daily(t, filter(from, until, "", 1, 20))
		same(t, got, []groupTotal{{utc(2031, 3, 11, 0, 0, 0, 0), 300, 1}, {utc(2031, 3, 10, 0, 0, 0, 0), 3500, 2}})
		if total != 2 {
			t.Fatalf("daily group total=%d", total)
		}
		got, total = monthly(t, filter(from, until, "", 1, 20))
		same(t, got, []groupTotal{{utc(2031, 3, 1, 0, 0, 0, 0), 3800, 3}})
		if total != 1 {
			t.Fatalf("monthly group total=%d", total)
		}
	})
	t.Run("SalespersonOwnership", func(t *testing.T) {
		from, until := year(2032)
		mine, other := uuid.NewString(), uuid.NewString()
		insert(t, mine, utc(2032, 5, 5, 8, 0, 0, 0), true, reportLine{product: reportProduct(1), quantity: 2, price: 100})
		insert(t, other, utc(2032, 5, 5, 9, 0, 0, 0), true, reportLine{product: reportProduct(2), quantity: 9, price: 100})
		insert(t, mine, utc(2032, 5, 6, 9, 0, 0, 0), false, reportLine{product: reportProduct(3), quantity: 4, price: 100})
		got, total := daily(t, filter(from, until, mine, 1, 20))
		same(t, got, []groupTotal{{utc(2032, 5, 5, 0, 0, 0, 0), 200, 1}})
		if total != 1 {
			t.Fatal(total)
		}
		got, _ = daily(t, filter(from, until, "", 1, 20))
		same(t, got, []groupTotal{{utc(2032, 5, 5, 0, 0, 0, 0), 1100, 2}})
		got, _ = monthly(t, filter(from, until, mine, 1, 20))
		same(t, got, []groupTotal{{utc(2032, 5, 1, 0, 0, 0, 0), 200, 1}})
		top, err := repo.TopProducts(ctx, filter(from, until, mine, 1, 20))
		if err != nil || top.Total != 1 || top.Items[0].ProductID != reportProduct(1) || top.Items[0].TotalQuantity != 2 {
			t.Fatalf("own top products=%+v err=%v", top, err)
		}
		none, err := repo.TopProducts(ctx, filter(from, until, uuid.NewString(), 1, 20))
		if err != nil || none.Total != 0 || len(none.Items) != 0 {
			t.Fatalf("unknown owner leaked rows: %+v err=%v", none, err)
		}
	})
	t.Run("UTCDayBoundaries", func(t *testing.T) {
		owner := uuid.NewString()
		bogota := time.FixedZone("Bogota", -5*3600)
		insert(t, owner, utc(2033, 6, 30, 23, 59, 59, 999), true, reportLine{product: reportProduct(1), quantity: 1, price: 10})
		insert(t, owner, utc(2033, 7, 1, 0, 0, 0, 0), true, reportLine{product: reportProduct(1), quantity: 1, price: 20})
		// 20:00 in Bogota on June 30 is already July 1 in UTC.
		insert(t, owner, time.Date(2033, 6, 30, 20, 0, 0, 0, bogota), true, reportLine{product: reportProduct(1), quantity: 1, price: 40})
		got, _ := daily(t, filter(utc(2033, 7, 1, 0, 0, 0, 0), utc(2033, 7, 2, 0, 0, 0, 0), owner, 1, 20))
		same(t, got, []groupTotal{{utc(2033, 7, 1, 0, 0, 0, 0), 60, 2}})
		got, _ = daily(t, filter(utc(2033, 6, 30, 0, 0, 0, 0), utc(2033, 7, 1, 0, 0, 0, 0), owner, 1, 20))
		same(t, got, []groupTotal{{utc(2033, 6, 30, 0, 0, 0, 0), 10, 1}})
		got, _ = daily(t, filter(utc(2033, 6, 30, 0, 0, 0, 0), utc(2033, 7, 2, 0, 0, 0, 0), owner, 1, 20))
		same(t, got, []groupTotal{{utc(2033, 7, 1, 0, 0, 0, 0), 60, 2}, {utc(2033, 6, 30, 0, 0, 0, 0), 10, 1}})
	})
	t.Run("MonthAndYearBoundaries", func(t *testing.T) {
		owner := uuid.NewString()
		line := reportLine{product: reportProduct(1), quantity: 1, price: 1}
		for _, d := range []time.Time{utc(2034, 12, 31, 23, 59, 59, 999), utc(2035, 1, 1, 0, 0, 0, 0), utc(2035, 1, 31, 23, 59, 59, 999), utc(2035, 2, 1, 0, 0, 0, 0), utc(2036, 2, 29, 12, 0, 0, 0), utc(2036, 3, 1, 0, 0, 0, 0)} {
			insert(t, owner, d, true, line)
		}
		got, total := monthly(t, filter(utc(2034, 12, 1, 0, 0, 0, 0), utc(2035, 3, 1, 0, 0, 0, 0), owner, 1, 20))
		same(t, got, []groupTotal{{utc(2035, 2, 1, 0, 0, 0, 0), 1, 1}, {utc(2035, 1, 1, 0, 0, 0, 0), 2, 2}, {utc(2034, 12, 1, 0, 0, 0, 0), 1, 1}})
		if total != 3 {
			t.Fatal(total)
		}
		got, _ = monthly(t, filter(utc(2036, 2, 1, 0, 0, 0, 0), utc(2036, 4, 1, 0, 0, 0, 0), owner, 1, 20))
		same(t, got, []groupTotal{{utc(2036, 3, 1, 0, 0, 0, 0), 1, 1}, {utc(2036, 2, 1, 0, 0, 0, 0), 1, 1}})
		got, _ = daily(t, filter(utc(2036, 2, 29, 0, 0, 0, 0), utc(2036, 3, 1, 0, 0, 0, 0), owner, 1, 20))
		same(t, got, []groupTotal{{utc(2036, 2, 29, 0, 0, 0, 0), 1, 1}})
	})
	t.Run("TopProductsLinesTiesAndInactiveDetails", func(t *testing.T) {
		from, until := year(2037)
		mine, other := uuid.NewString(), uuid.NewString()
		insert(t, mine, utc(2037, 4, 1, 10, 0, 0, 0), true,
			reportLine{product: reportProduct(1), quantity: 3, price: 100},
			reportLine{product: reportProduct(2), quantity: 1, price: 500},
			reportLine{product: reportProduct(1), quantity: 2, price: 100})
		insert(t, other, utc(2037, 4, 2, 10, 0, 0, 0), true,
			reportLine{product: reportProduct(1), quantity: 1, price: 100},
			reportLine{product: reportProduct(4), quantity: 5, price: 10},
			reportLine{product: reportProduct(3), quantity: 5, price: 10},
			reportLine{product: reportProduct(2), quantity: 9, price: 1, inactive: true})
		insert(t, other, utc(2037, 4, 3, 10, 0, 0, 0), false, reportLine{product: reportProduct(5), quantity: 50, price: 10})
		rows, err := repo.TopProducts(ctx, filter(from, until, "", 1, 20))
		if err != nil {
			t.Fatal(err)
		}
		want := []model.ProductSales{
			{ProductID: reportProduct(1), TotalQuantity: 6, TotalRevenueCents: 600},
			{ProductID: reportProduct(3), TotalQuantity: 5, TotalRevenueCents: 50},
			{ProductID: reportProduct(4), TotalQuantity: 5, TotalRevenueCents: 50},
			{ProductID: reportProduct(2), TotalQuantity: 1, TotalRevenueCents: 500},
		}
		if rows.Total != 4 || len(rows.Items) != len(want) {
			t.Fatalf("rows=%+v", rows)
		}
		for i := range want {
			if rows.Items[i] != want[i] {
				t.Fatalf("product %d: got=%+v want=%+v", i, rows.Items[i], want[i])
			}
		}
		own, err := repo.TopProducts(ctx, filter(from, until, mine, 1, 20))
		if err != nil || own.Total != 2 || own.Items[0] != (model.ProductSales{ProductID: reportProduct(1), TotalQuantity: 5, TotalRevenueCents: 500}) || own.Items[1] != (model.ProductSales{ProductID: reportProduct(2), TotalQuantity: 1, TotalRevenueCents: 500}) {
			t.Fatalf("own=%+v err=%v", own, err)
		}
	})
	t.Run("PaginationOverGroups", func(t *testing.T) {
		owner := uuid.NewString()
		from, until := year(2038)
		for day := 1; day <= 5; day++ {
			insert(t, owner, utc(2038, 8, day, 12, 0, 0, 0), true, reportLine{product: reportProduct(day), quantity: day, price: 10})
			// A second sale on the same day must not create a second group.
			insert(t, owner, utc(2038, 8, day, 18, 0, 0, 0), true, reportLine{product: reportProduct(day), quantity: 1, price: 10})
		}
		var days []time.Time
		for p, wantSize := range []int{2, 2, 1} {
			rows, err := repo.DailyTotals(ctx, filter(from, until, owner, int64(p+1), 2))
			if err != nil || rows.Total != 5 || len(rows.Items) != wantSize {
				t.Fatalf("page %d rows=%+v err=%v", p+1, rows, err)
			}
			for _, r := range rows.Items {
				if r.SaleCount != 2 {
					t.Fatalf("same-day sales not grouped: %+v", r)
				}
				days = append(days, r.Day)
			}
		}
		for i := range days {
			if !days[i].Equal(utc(2038, 8, 5-i, 0, 0, 0, 0)) {
				t.Fatalf("pages overlap or misorder: %v", days)
			}
		}
		beyond, err := repo.DailyTotals(ctx, filter(from, until, owner, 4, 2))
		if err != nil || beyond.Total != 5 || len(beyond.Items) != 0 {
			t.Fatalf("beyond-last page=%+v err=%v", beyond, err)
		}
		var products []string
		for p := int64(1); p <= 3; p++ {
			rows, err := repo.TopProducts(ctx, filter(from, until, owner, p, 2))
			if err != nil || rows.Total != 5 {
				t.Fatalf("top page %d rows=%+v err=%v", p, rows, err)
			}
			for _, r := range rows.Items {
				products = append(products, r.ProductID)
			}
		}
		for i, id := range products {
			if id != reportProduct(5-i) {
				t.Fatalf("top-product pages misordered: %v", products)
			}
		}
		months, err := repo.MonthlyTotals(ctx, filter(from, until, owner, 1, 1))
		if err != nil || months.Total != 1 || len(months.Items) != 1 || months.Items[0].SaleCount != 10 {
			t.Fatalf("months=%+v err=%v", months, err)
		}
	})
	t.Run("EmptyResults", func(t *testing.T) {
		f := filter(utc(2099, 1, 1, 0, 0, 0, 0), utc(2099, 2, 1, 0, 0, 0, 0), "", 1, 20)
		dailyRows, err := repo.DailyTotals(ctx, f)
		if err != nil || dailyRows.Total != 0 || dailyRows.Items == nil || len(dailyRows.Items) != 0 {
			t.Fatalf("daily=%+v err=%v", dailyRows, err)
		}
		monthlyRows, err := repo.MonthlyTotals(ctx, f)
		if err != nil || monthlyRows.Total != 0 || len(monthlyRows.Items) != 0 {
			t.Fatalf("monthly=%+v err=%v", monthlyRows, err)
		}
		topRows, err := repo.TopProducts(ctx, f)
		if err != nil || topRows.Total != 0 || len(topRows.Items) != 0 {
			t.Fatalf("top=%+v err=%v", topRows, err)
		}
	})
	t.Run("OverflowFailsSafely", func(t *testing.T) {
		from, until := year(2040)
		owner := uuid.NewString()
		insertAmounts(t, owner, utc(2040, 1, 1, 1, 0, 0, 0), math.MaxInt64, math.MaxInt64)
		insertAmounts(t, owner, utc(2040, 1, 1, 2, 0, 0, 0), math.MaxInt64, math.MaxInt64)
		f := filter(from, until, owner, 1, 20)
		if _, err := repo.DailyTotals(ctx, f); !errors.Is(err, ErrInvalidReportData) {
			t.Fatalf("daily overflow err=%v", err)
		}
		if _, err := repo.MonthlyTotals(ctx, f); !errors.Is(err, ErrInvalidReportData) {
			t.Fatalf("monthly overflow err=%v", err)
		}
		if _, err := repo.TopProducts(ctx, f); !errors.Is(err, ErrInvalidReportData) {
			t.Fatalf("top-products overflow err=%v", err)
		}
		// A single maximal sale is still exactly representable.
		single := uuid.NewString()
		insertAmounts(t, single, utc(2040, 2, 1, 1, 0, 0, 0), math.MaxInt64, math.MaxInt64)
		got, _ := daily(t, filter(from, until, single, 1, 20))
		same(t, got, []groupTotal{{utc(2040, 2, 1, 0, 0, 0, 0), math.MaxInt64, 1}})
	})
	t.Run("DatabaseFailureAndCancellation", func(t *testing.T) {
		unavailable := NewMongoRepository(integrationClient(t, "mongodb://127.0.0.1:1/sales?directConnection=true").Database("sales").Collection("sale"))
		f := filter(utc(2031, 1, 1, 0, 0, 0, 0), utc(2032, 1, 1, 0, 0, 0, 0), "", 1, 20)
		if _, err := unavailable.DailyTotals(ctx, f); err == nil {
			t.Fatal("daily masked an unavailable database")
		}
		if _, err := unavailable.MonthlyTotals(ctx, f); err == nil {
			t.Fatal("monthly masked an unavailable database")
		}
		if _, err := unavailable.TopProducts(ctx, f); err == nil {
			t.Fatal("top products masked an unavailable database")
		}
		cancelled, stop := context.WithCancel(ctx)
		stop()
		if _, err := repo.DailyTotals(cancelled, f); err == nil {
			t.Fatal("cancelled context ignored")
		}
	})
}
