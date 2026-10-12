package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ out.SalesReportRepository = (*MongoRepository)(nil)

// ErrInvalidReportData means an aggregate result cannot be represented
// faithfully, for example a $sum that overflowed int64 into a double.
var ErrInvalidReportData = errors.New("report aggregate cannot be represented faithfully")

func invalidReport(reason string) error { return fmt.Errorf("%w: %s", ErrInvalidReportData, reason) }

// Each pipeline filters first so idx_sale_date / idx_sale_created_by_date
// serve the range and ownership (ADR-010 Decision 4), then groups inside the
// database. A single $facet returns the requested page and the count of all
// groups from one snapshot, so data and meta.total cannot disagree. The page
// is cut after $group and $sort, never before.

func reportMatch(f out.ReportFilter) bson.D {
	return bson.D{{Key: "$match", Value: activeFilter(out.SaleFilter{CreatedBy: f.CreatedBy, From: f.From, Until: f.Until})}}
}

func reportFacet(f out.ReportFilter, order bson.D) bson.D {
	return bson.D{{Key: "$facet", Value: bson.D{
		{Key: "items", Value: bson.A{
			bson.D{{Key: "$sort", Value: order}},
			bson.D{{Key: "$skip", Value: (f.Page - 1) * f.Limit}},
			bson.D{{Key: "$limit", Value: f.Limit}},
		}},
		{Key: "total", Value: bson.A{bson.D{{Key: "$count", Value: "n"}}}},
	}}}
}

// periodGroup groups sales by their UTC calendar unit ("day" or "month");
// $dateTrunc already normalizes a month to its first day at midnight.
func periodGroup(unit string) bson.D {
	return bson.D{{Key: "$group", Value: bson.D{
		{Key: "_id", Value: bson.D{{Key: "$dateTrunc", Value: bson.D{{Key: "date", Value: "$date"}, {Key: "unit", Value: unit}, {Key: "timezone", Value: "UTC"}}}}},
		{Key: "totalCents", Value: bson.D{{Key: "$sum", Value: "$totalCents"}}},
		{Key: "saleCount", Value: bson.D{{Key: "$sum", Value: 1}}},
	}}}
}

func newestFirst() bson.D { return bson.D{{Key: "_id", Value: -1}} }

func dailyPipeline(f out.ReportFilter) mongo.Pipeline {
	return mongo.Pipeline{reportMatch(f), periodGroup("day"), reportFacet(f, newestFirst())}
}

func monthlyPipeline(f out.ReportFilter) mongo.Pipeline {
	return mongo.Pipeline{reportMatch(f), periodGroup("month"), reportFacet(f, newestFirst())}
}

func topProductsPipeline(f out.ReportFilter) mongo.Pipeline {
	return mongo.Pipeline{
		reportMatch(f),
		bson.D{{Key: "$unwind", Value: "$details"}},
		// ADR-010 Decision 4: keep active lines; a voided line never counts.
		bson.D{{Key: "$match", Value: bson.D{{Key: "details.active", Value: true}}}},
		bson.D{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: "$details.productId"},
			{Key: "totalQuantity", Value: bson.D{{Key: "$sum", Value: "$details.quantity"}}},
			{Key: "totalRevenueCents", Value: bson.D{{Key: "$sum", Value: "$details.subtotalCents"}}},
		}}},
		reportFacet(f, bson.D{{Key: "totalQuantity", Value: -1}, {Key: "_id", Value: 1}}),
	}
}

func (r *MongoRepository) aggregate(ctx context.Context, name string, pipeline mongo.Pipeline) (bson.Raw, error) {
	cursor, err := r.collection.Aggregate(ctx, pipeline, options.Aggregate().SetAllowDiskUse(true))
	if err != nil {
		return nil, fmt.Errorf("aggregate %s: %w", name, err)
	}
	defer cursor.Close(ctx)
	if !cursor.Next(ctx) {
		if err := cursor.Err(); err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		return nil, invalidReport("aggregation returned no result document")
	}
	raw := append(bson.Raw(nil), cursor.Current...)
	if cursor.Next(ctx) {
		return nil, invalidReport("aggregation returned more than one result document")
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	return raw, nil
}

func (r *MongoRepository) DailyTotals(ctx context.Context, f out.ReportFilter) (out.ReportRows[model.DailyTotal], error) {
	raw, err := r.aggregate(ctx, "daily totals", dailyPipeline(f))
	if err != nil {
		return out.ReportRows[model.DailyTotal]{}, err
	}
	return decodeDailyRows(raw)
}

func (r *MongoRepository) MonthlyTotals(ctx context.Context, f out.ReportFilter) (out.ReportRows[model.MonthlyTotal], error) {
	raw, err := r.aggregate(ctx, "monthly totals", monthlyPipeline(f))
	if err != nil {
		return out.ReportRows[model.MonthlyTotal]{}, err
	}
	return decodeMonthlyRows(raw)
}

func (r *MongoRepository) TopProducts(ctx context.Context, f out.ReportFilter) (out.ReportRows[model.ProductSales], error) {
	raw, err := r.aggregate(ctx, "top products", topProductsPipeline(f))
	if err != nil {
		return out.ReportRows[model.ProductSales]{}, err
	}
	return decodeTopProductRows(raw)
}

func decodeDailyRows(raw bson.Raw) (out.ReportRows[model.DailyTotal], error) {
	return decodeRows(raw, func(doc bson.Raw) (model.DailyTotal, error) {
		day, total, count, err := decodePeriod(doc)
		if err != nil {
			return model.DailyTotal{}, err
		}
		return model.DailyTotal{Day: day, TotalCents: total, SaleCount: count}, nil
	})
}

func decodeMonthlyRows(raw bson.Raw) (out.ReportRows[model.MonthlyTotal], error) {
	return decodeRows(raw, func(doc bson.Raw) (model.MonthlyTotal, error) {
		month, total, count, err := decodePeriod(doc)
		if err != nil {
			return model.MonthlyTotal{}, err
		}
		if month.Day() != 1 {
			return model.MonthlyTotal{}, invalidReport("month is not the first day")
		}
		return model.MonthlyTotal{Month: month, TotalCents: total, SaleCount: count}, nil
	})
}

func decodeTopProductRows(raw bson.Raw) (out.ReportRows[model.ProductSales], error) {
	return decodeRows(raw, func(doc bson.Raw) (model.ProductSales, error) {
		fields, err := reportFields(doc, "_id", "totalQuantity", "totalRevenueCents")
		if err != nil {
			return model.ProductSales{}, err
		}
		id, ok := fields["_id"].StringValueOK()
		if !ok || !canonicalUUID(id) {
			return model.ProductSales{}, invalidReport("product is not a canonical UUID")
		}
		quantity, err := nonNegative(fields["totalQuantity"])
		if err != nil {
			return model.ProductSales{}, err
		}
		revenue, err := nonNegative(fields["totalRevenueCents"])
		if err != nil {
			return model.ProductSales{}, err
		}
		if quantity == 0 {
			return model.ProductSales{}, invalidReport("product group without units")
		}
		return model.ProductSales{ProductID: id, TotalQuantity: quantity, TotalRevenueCents: revenue}, nil
	})
}

// decodePeriod reads a day or month group; the key is a UTC midnight date.
func decodePeriod(doc bson.Raw) (time.Time, int64, int64, error) {
	fields, err := reportFields(doc, "_id", "totalCents", "saleCount")
	if err != nil {
		return time.Time{}, 0, 0, err
	}
	millis, ok := fields["_id"].DateTimeOK()
	if !ok {
		return time.Time{}, 0, 0, invalidReport("group key is not a date")
	}
	at := time.UnixMilli(millis).UTC()
	if at.Hour() != 0 || at.Minute() != 0 || at.Second() != 0 || at.Nanosecond() != 0 {
		return time.Time{}, 0, 0, invalidReport("group key is not a UTC midnight")
	}
	total, err := nonNegative(fields["totalCents"])
	if err != nil {
		return time.Time{}, 0, 0, err
	}
	count, err := nonNegative(fields["saleCount"])
	if err != nil {
		return time.Time{}, 0, 0, err
	}
	return at, total, count, nil
}

// reportFields requires exactly the named fields, rejecting extras and
// duplicates, so an unexpected shape is never silently reinterpreted.
func reportFields(doc bson.Raw, names ...string) (map[string]bson.RawValue, error) {
	elements, err := doc.Elements()
	if err != nil || len(elements) != len(names) {
		return nil, invalidReport("unexpected group shape")
	}
	fields := make(map[string]bson.RawValue, len(names))
	for _, element := range elements {
		fields[element.Key()] = element.Value()
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return nil, invalidReport("missing group field " + name)
		}
	}
	if len(fields) != len(names) {
		return nil, invalidReport("duplicate group field")
	}
	return fields, nil
}

// nonNegative accepts only integer BSON types. MongoDB's $sum silently turns
// an int64 overflow into a double, which must fail instead of being rounded.
func nonNegative(v bson.RawValue) (int64, error) {
	var n int64
	switch v.Type {
	case bson.TypeInt32:
		n = int64(v.Int32())
	case bson.TypeInt64:
		n = v.Int64()
	default:
		return 0, invalidReport("aggregate is not an integer (possible overflow)")
	}
	if n < 0 {
		return 0, invalidReport("negative aggregate")
	}
	return n, nil
}

// decodeRows reads the single $facet document: {items: [...], total: [{n}]}.
func decodeRows[T any](raw bson.Raw, item func(bson.Raw) (T, error)) (out.ReportRows[T], error) {
	none := out.ReportRows[T]{}
	if err := raw.Validate(); err != nil {
		return none, invalidReport("malformed BSON")
	}
	facets, err := reportFields(raw, "items", "total")
	if err != nil {
		return none, err
	}
	counts, ok := facets["total"].ArrayOK()
	if !ok {
		return none, invalidReport("total is not an array")
	}
	countValues, err := counts.Values()
	if err != nil || len(countValues) > 1 {
		return none, invalidReport("unexpected total shape")
	}
	var total int64
	if len(countValues) == 1 {
		doc, ok := countValues[0].DocumentOK()
		if !ok {
			return none, invalidReport("total is not a document")
		}
		fields, err := reportFields(doc, "n")
		if err != nil {
			return none, err
		}
		if total, err = nonNegative(fields["n"]); err != nil {
			return none, err
		}
	}
	list, ok := facets["items"].ArrayOK()
	if !ok {
		return none, invalidReport("items is not an array")
	}
	values, err := list.Values()
	if err != nil || int64(len(values)) > total {
		return none, invalidReport("items inconsistent with total")
	}
	items := make([]T, 0, len(values))
	for _, value := range values {
		doc, ok := value.DocumentOK()
		if !ok {
			return none, invalidReport("group is not a document")
		}
		decoded, err := item(doc)
		if err != nil {
			return none, err
		}
		items = append(items, decoded)
	}
	return out.ReportRows[T]{Items: items, Total: total}, nil
}
