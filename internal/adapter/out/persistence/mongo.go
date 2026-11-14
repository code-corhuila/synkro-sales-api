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

type MongoRepository struct{ collection, counters *mongo.Collection }

var _ out.SaleRepository = (*MongoRepository)(nil)

// NewMongoRepository receives sales.sale and sales.sale_counter from the composition
// root's shared sales_app client. Migrations, indexes and client lifetime belong to the caller.
func NewMongoRepository(collection, counters *mongo.Collection) *MongoRepository {
	return &MongoRepository{collection: collection, counters: counters}
}

// validatorFailure is MongoDB's DocumentValidationFailure. On the counter it means the
// increment would exceed the six-digit ceiling enforced by the database.
const validatorFailure = 121

// nextCommercialNumber atomically advances the counter of the sale's calendar year.
// A number is consumed even if the following insert is lost to a concurrent replay or
// a failure, so gaps are possible; uniqueness is what is guaranteed.
func (r *MongoRepository) nextCommercialNumber(ctx context.Context, sale *model.Sale) (string, error) {
	year := model.CommercialYear(sale.Date())
	var counter struct {
		Seq int64 `bson:"seq"`
	}
	err := r.counters.FindOneAndUpdate(ctx,
		bson.D{{Key: "_id", Value: fmt.Sprintf("VEN-%04d", year)}},
		bson.D{{Key: "$inc", Value: bson.D{{Key: "seq", Value: int64(1)}}}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&counter)
	if err != nil {
		var cmdErr mongo.CommandError
		var writeErr mongo.WriteException
		if (errors.As(err, &cmdErr) && cmdErr.Code == validatorFailure) ||
			(errors.As(err, &writeErr) && len(writeErr.WriteErrors) > 0 && writeErr.WriteErrors[0].Code == validatorFailure) {
			return "", model.ErrCommercialSequenceExhausted
		}
		return "", fmt.Errorf("advance commercial counter: %w", err)
	}
	return model.NewCommercialNumber(year, counter.Seq)
}

func (r *MongoRepository) CreateOnce(ctx context.Context, key string, sale *model.Sale) (*model.Sale, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	// An unrepresentable sale is rejected before the database is touched or a number is spent.
	if _, err := encodeSale(key, sale); err != nil {
		return nil, false, err
	}
	// A replay must not consume a number: look the key up before allocating.
	original, err := r.collection.FindOne(ctx, bson.D{{Key: "idempotencyKey", Value: key}}).Raw()
	if err == nil {
		replayed, err := decodeSale(original)
		if err != nil {
			return nil, false, err
		}
		return replayed, false, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, false, fmt.Errorf("find original sale: %w", err)
	}
	if sale != nil && sale.CommercialNumber() == "" {
		number, err := r.nextCommercialNumber(ctx, sale)
		if err != nil {
			return nil, false, err
		}
		if err := sale.AssignCommercialNumber(number); err != nil {
			return nil, false, err
		}
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
	original, err = r.collection.FindOne(ctx, bson.D{{Key: "idempotencyKey", Value: key}}).Raw()
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
