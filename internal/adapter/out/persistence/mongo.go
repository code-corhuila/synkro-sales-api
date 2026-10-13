package persistence

import (
	"context"
	"errors"
	"fmt"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type MongoRepository struct{ collection *mongo.Collection }

var _ out.SaleRepository = (*MongoRepository)(nil)

// NewMongoRepository receives sales.sale from the composition root's shared
// sales_app client. Migrations, indexes and client lifetime belong to the caller.
func NewMongoRepository(collection *mongo.Collection) *MongoRepository {
	return &MongoRepository{collection: collection}
}

func (r *MongoRepository) CreateOnce(ctx context.Context, key string, sale *model.Sale) (*model.Sale, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	raw, err := encodeSale(key, sale)
	if err != nil {
		return nil, false, err
	}
	stored, err := decodeSale(raw)
	if err != nil {
		return nil, false, err
	}
	_, insertErr := r.collection.InsertOne(ctx, raw)
	if insertErr == nil {
		return stored, true, nil
	}
	if !mongo.IsDuplicateKeyError(insertErr) {
		return nil, false, fmt.Errorf("insert sale: %w", insertErr)
	}
	original, err := r.collection.FindOne(ctx, bson.D{{Key: "idempotencyKey", Value: key}}).Raw()
	if errors.Is(err, mongo.ErrNoDocuments) {
		// A different unique constraint (including _id_) is not a replay.
		return nil, false, fmt.Errorf("duplicate sale without matching idempotency key: %w", insertErr)
	}
	if err != nil {
		return nil, false, fmt.Errorf("find original sale: %w", err)
	}
	stored, err = decodeSale(original)
	if err != nil {
		return nil, false, err
	}
	return stored, false, nil
}
