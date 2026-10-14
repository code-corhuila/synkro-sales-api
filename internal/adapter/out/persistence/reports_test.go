package persistence

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	boundFrom  = time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	boundUntil = time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
)

func stageNames(p mongo.Pipeline) []string {
	names := make([]string, len(p))
	for i, stage := range p {
		names[i] = stage[0].Key
	}
	return names
}

func stageJSON(t *testing.T, stage bson.D) string {
	t.Helper()
	text, err := bson.MarshalExtJSON(stage, false, false)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestReportPipelinesFilterBeforeGrouping(t *testing.T) {
	filter := out.ReportFilter{CreatedBy: "550e8400-e29b-41d4-a716-446655440000", From: &boundFrom, Until: &boundUntil, Page: 3, Limit: 25}
	cases := map[string]struct {
		pipeline mongo.Pipeline
		stages   []string
	}{
		"daily":        {dailyPipeline(filter), []string{"$match", "$group", "$facet"}},
		"monthly":      {monthlyPipeline(filter), []string{"$match", "$group", "$facet"}},
		"top-products": {topProductsPipeline(filter), []string{"$match", "$unwind", "$match", "$group", "$facet"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := strings.Join(stageNames(c.pipeline), ","); got != strings.Join(c.stages, ",") {
				t.Fatalf("stages=%s want=%v", got, c.stages)
			}
			match := stageJSON(t, c.pipeline[0])
			for _, want := range []string{`"active":true`, `"createdBy":"550e8400-e29b-41d4-a716-446655440000"`, `"$gte"`, `"$lt"`} {
				if !strings.Contains(match, want) {
					t.Fatalf("first stage must filter on %s: %s", want, match)
				}
			}
			facet := stageJSON(t, c.pipeline[len(c.pipeline)-1])
			for _, want := range []string{`"$skip":50`, `"$limit":25`, `"$count"`} {
				if !strings.Contains(facet, want) {
					t.Fatalf("facet must contain %s: %s", want, facet)
				}
			}
		})
	}
}

func TestReportPipelinesOmitOwnershipAndBoundsWhenAbsent(t *testing.T) {
	for name, pipeline := range map[string]mongo.Pipeline{
		"daily":        dailyPipeline(out.ReportFilter{Page: 1, Limit: 20}),
		"monthly":      monthlyPipeline(out.ReportFilter{Page: 1, Limit: 20}),
		"top-products": topProductsPipeline(out.ReportFilter{Page: 1, Limit: 20}),
	} {
		match := stageJSON(t, pipeline[0])
		if strings.Contains(match, "createdBy") || strings.Contains(match, `"date"`) || !strings.Contains(match, `"active":true`) {
			t.Fatalf("%s: unfiltered first stage=%s", name, match)
		}
	}
}

func TestReportPipelinesGroupAndOrderDeterministically(t *testing.T) {
	filter := out.ReportFilter{Page: 1, Limit: 20}
	daily, monthly := stageJSON(t, dailyPipeline(filter)[1]), stageJSON(t, monthlyPipeline(filter)[1])
	if !strings.Contains(daily, `"unit":"day"`) || !strings.Contains(daily, `"timezone":"UTC"`) || !strings.Contains(daily, `"$sum":"$totalCents"`) || !strings.Contains(daily, `"saleCount":{"$sum":1}`) {
		t.Fatalf("daily group=%s", daily)
	}
	if !strings.Contains(monthly, `"unit":"month"`) || !strings.Contains(monthly, `"timezone":"UTC"`) || !strings.Contains(monthly, `"$sum":"$totalCents"`) {
		t.Fatalf("monthly group=%s", monthly)
	}
	for name, pipeline := range map[string]mongo.Pipeline{"daily": dailyPipeline(filter), "monthly": monthlyPipeline(filter)} {
		if facet := stageJSON(t, pipeline[2]); !strings.Contains(facet, `"$sort":{"_id":-1}`) {
			t.Fatalf("%s must sort newest first: %s", name, facet)
		}
	}
	top := topProductsPipeline(filter)
	if unwind := stageJSON(t, top[1]); !strings.Contains(unwind, `"$unwind":"$details"`) {
		t.Fatalf("unwind=%s", unwind)
	}
	if lines := stageJSON(t, top[2]); !strings.Contains(lines, `"details.active":true`) {
		t.Fatalf("inactive lines must be dropped before grouping: %s", lines)
	}
	if group := stageJSON(t, top[3]); !strings.Contains(group, `"_id":"$details.productId"`) || !strings.Contains(group, `"$sum":"$details.quantity"`) || !strings.Contains(group, `"$sum":"$details.subtotalCents"`) {
		t.Fatalf("group=%s", group)
	}
	if facet := stageJSON(t, top[4]); !strings.Contains(facet, `"$sort":{"totalQuantity":-1,"_id":1}`) {
		t.Fatalf("deterministic product order missing: %s", facet)
	}
}

func facetDocument(items bson.A, total ...int32) bson.Raw {
	counted := bson.A{}
	for _, n := range total {
		counted = append(counted, bson.D{{Key: "n", Value: n}})
	}
	raw, _ := bson.Marshal(bson.D{{Key: "items", Value: items}, {Key: "total", Value: counted}})
	return raw
}

func dayDoc(day time.Time, total any, count any) bson.D {
	return bson.D{{Key: "_id", Value: bson.NewDateTimeFromTime(day)}, {Key: "totalCents", Value: total}, {Key: "saleCount", Value: count}}
}

func TestDecodeDailyRows(t *testing.T) {
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	rows, err := decodeDailyRows(facetDocument(bson.A{dayDoc(day, int64(250000), int32(2))}, 7))
	if err != nil || rows.Total != 7 || len(rows.Items) != 1 || !rows.Items[0].Day.Equal(day) || rows.Items[0].Day.Location() != time.UTC || rows.Items[0].TotalCents != 250000 || rows.Items[0].SaleCount != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	empty, err := decodeDailyRows(facetDocument(bson.A{}))
	if err != nil || empty.Total != 0 || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty=%+v err=%v", empty, err)
	}
}

func TestDecodeMonthlyRows(t *testing.T) {
	month := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	rows, err := decodeMonthlyRows(facetDocument(bson.A{dayDoc(month, int64(99), int32(1))}, 1))
	if err != nil || rows.Total != 1 || !rows.Items[0].Month.Equal(month) || rows.Items[0].TotalCents != 99 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
}

func TestDecodeTopProductRows(t *testing.T) {
	id := "550e8400-e29b-41d4-a716-446655440003"
	doc := bson.D{{Key: "_id", Value: id}, {Key: "totalQuantity", Value: int32(6)}, {Key: "totalRevenueCents", Value: int64(600)}}
	rows, err := decodeTopProductRows(facetDocument(bson.A{doc}, 1))
	if err != nil || len(rows.Items) != 1 || rows.Items[0].ProductID != id || rows.Items[0].TotalQuantity != 6 || rows.Items[0].TotalRevenueCents != 600 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	// $sum widens int32 quantities to int64 once they exceed the int32 range.
	wide := bson.D{{Key: "_id", Value: id}, {Key: "totalQuantity", Value: int64(5_000_000_000)}, {Key: "totalRevenueCents", Value: int64(1)}}
	if rows, err = decodeTopProductRows(facetDocument(bson.A{wide}, 1)); err != nil || rows.Items[0].TotalQuantity != 5_000_000_000 {
		t.Fatalf("wide quantity rows=%+v err=%v", rows, err)
	}
}

func TestReportDecodingRejectsUnfaithfulAggregates(t *testing.T) {
	day := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	month := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	id := "550e8400-e29b-41d4-a716-446655440003"
	product := func(overrides ...bson.E) bson.D {
		doc := bson.D{{Key: "_id", Value: id}, {Key: "totalQuantity", Value: int32(1)}, {Key: "totalRevenueCents", Value: int64(1)}}
		for _, o := range overrides {
			for i := range doc {
				if doc[i].Key == o.Key {
					doc[i].Value = o.Value
				}
			}
		}
		return doc
	}
	daily := map[string]bson.Raw{
		"sum overflowed to double": facetDocument(bson.A{dayDoc(day, 9.3e18, int32(1))}, 1),
		"string total":             facetDocument(bson.A{dayDoc(day, "10", int32(1))}, 1),
		"double count":             facetDocument(bson.A{dayDoc(day, int64(1), 1.5)}, 1),
		"negative total":           facetDocument(bson.A{dayDoc(day, int64(-1), int32(1))}, 1),
		"negative count":           facetDocument(bson.A{dayDoc(day, int64(1), int32(-1))}, 1),
		"day not midnight":         facetDocument(bson.A{dayDoc(day.Add(time.Hour), int64(1), int32(1))}, 1),
		"day is a string":          facetDocument(bson.A{bson.D{{Key: "_id", Value: "2026-10-12"}, {Key: "totalCents", Value: int64(1)}, {Key: "saleCount", Value: int32(1)}}}, 1),
		"missing count":            facetDocument(bson.A{bson.D{{Key: "_id", Value: bson.NewDateTimeFromTime(day)}, {Key: "totalCents", Value: int64(1)}}}, 1),
		"unexpected field":         facetDocument(bson.A{append(dayDoc(day, int64(1), int32(1)), bson.E{Key: "extra", Value: 1})}, 1),
		"item not a document":      facetDocument(bson.A{"x"}, 1),
		"negative group count":     facetDocument(bson.A{}, -1),
		"two count documents":      facetDocument(bson.A{}, 1, 2),
		"items fewer than total":   facetDocument(bson.A{dayDoc(day, int64(1), int32(1)), dayDoc(day, int64(1), int32(1))}, 1),
		"not a BSON document":      bson.Raw{1, 2, 3},
		"missing facets":           mustRaw(t, bson.D{{Key: "items", Value: bson.A{}}}),
	}
	for name, raw := range daily {
		t.Run("daily "+name, func(t *testing.T) {
			if _, err := decodeDailyRows(raw); !errors.Is(err, ErrInvalidReportData) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	monthly := map[string]bson.Raw{
		"not first day of month": facetDocument(bson.A{dayDoc(day, int64(1), int32(1))}, 1),
		"double total":           facetDocument(bson.A{dayDoc(month, 1.0, int32(1))}, 1),
	}
	for name, raw := range monthly {
		t.Run("monthly "+name, func(t *testing.T) {
			if _, err := decodeMonthlyRows(raw); !errors.Is(err, ErrInvalidReportData) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	top := map[string]bson.Raw{
		"noncanonical product":  facetDocument(bson.A{product(bson.E{Key: "_id", Value: "not-a-uuid"})}, 1),
		"uppercase product":     facetDocument(bson.A{product(bson.E{Key: "_id", Value: strings.ToUpper(id)})}, 1),
		"product is not string": facetDocument(bson.A{product(bson.E{Key: "_id", Value: int32(7)})}, 1),
		"zero quantity":         facetDocument(bson.A{product(bson.E{Key: "totalQuantity", Value: int32(0)})}, 1),
		"double quantity":       facetDocument(bson.A{product(bson.E{Key: "totalQuantity", Value: 2.0})}, 1),
		"double revenue":        facetDocument(bson.A{product(bson.E{Key: "totalRevenueCents", Value: 9.3e18})}, 1),
		"negative revenue":      facetDocument(bson.A{product(bson.E{Key: "totalRevenueCents", Value: int64(-3)})}, 1),
		"null product":          facetDocument(bson.A{product(bson.E{Key: "_id", Value: nil})}, 1),
	}
	for name, raw := range top {
		t.Run("top-products "+name, func(t *testing.T) {
			if _, err := decodeTopProductRows(raw); !errors.Is(err, ErrInvalidReportData) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func mustRaw(t *testing.T, doc bson.D) bson.Raw {
	t.Helper()
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
