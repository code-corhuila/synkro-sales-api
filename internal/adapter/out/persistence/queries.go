package persistence

import (
	"context"
	"errors"
	"fmt"
	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

var _ out.SaleReadRepository = (*MongoRepository)(nil)

func activeFilter(f out.SaleFilter) bson.D {
	filter := bson.D{{Key: "active", Value: true}}
	if f.CustomerID != "" {
		filter = append(filter, bson.E{Key: "customerId", Value: f.CustomerID})
	}
	if f.CreatedBy != "" {
		filter = append(filter, bson.E{Key: "createdBy", Value: f.CreatedBy})
	}
	bounds := bson.D{}
	if f.From != nil {
		bounds = append(bounds, bson.E{Key: "$gte", Value: *f.From})
	}
	if f.Until != nil {
		bounds = append(bounds, bson.E{Key: "$lt", Value: *f.Until})
	}
	if len(bounds) > 0 {
		filter = append(filter, bson.E{Key: "date", Value: bounds})
	}
	return filter
}
func (r *MongoRepository) FindActiveByID(ctx context.Context, id string) (*model.Sale, error) {
	raw, err := r.collection.FindOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "active", Value: true}}).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, out.ErrSaleNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("find active sale: %w", err)
	}
	return decodeSale(raw)
}
func (r *MongoRepository) ListActive(ctx context.Context, f out.SaleFilter) ([]*model.Sale, error) {
	// _id is unique and provides deterministic pagination for equal timestamps.
	cursor, err := r.collection.Find(ctx, activeFilter(f), options.Find().SetSort(bson.D{{Key: "date", Value: -1}, {Key: "_id", Value: -1}}).SetSkip((f.Page-1)*f.Limit).SetLimit(f.Limit))
	if err != nil {
		return nil, fmt.Errorf("list active sales: %w", err)
	}
	defer cursor.Close(ctx)
	sales := []*model.Sale{}
	for cursor.Next(ctx) {
		sale, err := decodeSale(cursor.Current)
		if err != nil {
			return nil, err
		}
		sales = append(sales, sale)
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("read active sales: %w", err)
	}
	return sales, nil
}
func (r *MongoRepository) CountActive(ctx context.Context, f out.SaleFilter) (int64, error) {
	total, err := r.collection.CountDocuments(ctx, activeFilter(f))
	if err != nil {
		return 0, fmt.Errorf("count active sales: %w", err)
	}
	return total, nil
}
