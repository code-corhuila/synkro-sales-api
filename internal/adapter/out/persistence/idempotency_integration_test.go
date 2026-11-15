package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/out"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// equivalentSale is a new attempt for the same request: the same business input with
// freshly generated identifiers, as the use case builds on every retry.
func equivalentSale(t *testing.T, of *model.Sale, at time.Time) *model.Sale {
	t.Helper()
	return variantSale(t, of, at, nil)
}

// variantSale is equivalentSale with one deliberate difference in the business input.
func variantSale(t *testing.T, of *model.Sale, at time.Time, change func(customer, author *string, lines []model.LineInput)) *model.Sale {
	t.Helper()
	customer, author := of.CustomerID(), of.CreatedBy()
	lines := make([]model.LineInput, 0, len(of.Details()))
	for _, d := range of.Details() {
		lines = append(lines, model.LineInput{DetailID: uuid.NewString(), ProductID: d.ProductID(), Quantity: d.Quantity(), UnitPriceCents: d.UnitPriceCents()})
	}
	if change != nil {
		change(&customer, &author, lines)
	}
	s, err := model.RegisterSale(uuid.NewString(), customer, author, at, lines)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// runIdempotencyIntegration (issue 285) proves against the real database that a reused key
// returns the original only for the same business input, on the early lookup and in the race.
func runIdempotencyIntegration(t *testing.T, client *mongo.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	collection := client.Database("sales").Collection("sale")
	repo := NewMongoRepository(collection, client.Database("sales").Collection("sale_counter"))
	newKey := func() string { return uuid.NewString() + ":register-sale" }
	at := time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC)
	count := func(t *testing.T, key string) int64 {
		t.Helper()
		n, err := collection.CountDocuments(ctx, bson.M{"idempotencyKey": key})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	counterSeq := func(t *testing.T) int64 {
		t.Helper()
		var c struct {
			Seq int64 `bson:"seq"`
		}
		err := client.Database("sales").Collection("sale_counter").FindOne(ctx, bson.M{"_id": "VEN-2026"}).Decode(&c)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return c.Seq
	}

	t.Run("ConflictingInputIsRejectedAndOriginalKept", func(t *testing.T) {
		original := saleFixture(t, 2)
		key := newKey()
		first, created, err := repo.CreateOnce(ctx, key, original)
		if err != nil || !created {
			t.Fatalf("first insertion: created=%v err=%v", created, err)
		}
		changes := map[string]func(customer, author *string, lines []model.LineInput){
			"customer": func(c, _ *string, _ []model.LineInput) { *c = uuid.NewString() },
			"actor":    func(_, a *string, _ []model.LineInput) { *a = uuid.NewString() },
			"quantity": func(_, _ *string, l []model.LineInput) { l[0].Quantity++ },
			"price":    func(_, _ *string, l []model.LineInput) { l[0].UnitPriceCents = 9000 },
			"product":  func(_, _ *string, l []model.LineInput) { l[0].ProductID = uuid.NewString() },
		}
		for name, change := range changes {
			before := counterSeq(t)
			got, created, err := repo.CreateOnce(ctx, key, variantSale(t, original, at, change))
			if !errors.Is(err, out.ErrIdempotencyConflict) || got != nil || created {
				t.Fatalf("%s: got=%v created=%v err=%v, want ErrIdempotencyConflict", name, got, created, err)
			}
			if after := counterSeq(t); after != before {
				t.Fatalf("%s: a conflicting retry consumed a commercial number (%d -> %d)", name, before, after)
			}
		}
		if count(t, key) != 1 {
			t.Fatal("a conflicting retry changed the number of stored sales")
		}
		raw, err := collection.FindOne(ctx, bson.M{"idempotencyKey": key}).Raw()
		if err != nil {
			t.Fatal(err)
		}
		stored, err := decodeSale(raw)
		if err != nil {
			t.Fatal(err)
		}
		assertSale(t, stored, first)
	})

	t.Run("ExactAndReorderedRetriesReturnTheOriginalOnce", func(t *testing.T) {
		original := saleFixture(t, 3)
		key := newKey()
		first, created, err := repo.CreateOnce(ctx, key, original)
		if err != nil || !created {
			t.Fatalf("first insertion: created=%v err=%v", created, err)
		}
		details := original.Details()
		reordered := make([]model.LineInput, 0, len(details))
		for i := len(details) - 1; i >= 0; i-- {
			reordered = append(reordered, model.LineInput{DetailID: uuid.NewString(), ProductID: details[i].ProductID(), Quantity: details[i].Quantity(), UnitPriceCents: details[i].UnitPriceCents()})
		}
		other, err := model.RegisterSale(uuid.NewString(), original.CustomerID(), original.CreatedBy(), at.Add(time.Hour), reordered)
		if err != nil {
			t.Fatal(err)
		}
		for name, attempt := range map[string]*model.Sale{"exact": equivalentSale(t, original, at), "reordered": other} {
			got, created, err := repo.CreateOnce(ctx, key, attempt)
			if err != nil || created {
				t.Fatalf("%s: created=%v err=%v", name, created, err)
			}
			assertSale(t, got, first)
		}
		if count(t, key) != 1 {
			t.Fatal("a retry inserted another sale")
		}
	})

	t.Run("ConcurrentConflictingInsertsPersistExactlyOne", func(t *testing.T) {
		const n = 24
		key := newKey()
		base := saleFixture(t, 1)
		type outcome struct {
			sale     *model.Sale
			created  bool
			err      error
			variant  bool
			attempts *model.Sale
		}
		results := make(chan outcome, n)
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := 0; i < n; i++ {
			attempt, variant := equivalentSale(t, base, at), i%2 == 1
			if variant {
				attempt = variantSale(t, base, at, func(_, _ *string, l []model.LineInput) { l[0].UnitPriceCents = 7777 })
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				got, created, err := repo.CreateOnce(ctx, key, attempt)
				results <- outcome{got, created, err, variant, attempt}
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		created, winnerIsVariant := 0, false
		var winner *model.Sale
		var failures []outcome
		for r := range results {
			if r.err == nil && r.created {
				created++
				winnerIsVariant, winner = r.variant, r.sale
			}
			if r.err != nil {
				failures = append(failures, r)
			}
		}
		if created != 1 || count(t, key) != 1 {
			t.Fatalf("created=%d stored=%d, want exactly one", created, count(t, key))
		}
		raw, err := collection.FindOne(ctx, bson.M{"idempotencyKey": key}).Raw()
		if err != nil {
			t.Fatal(err)
		}
		stored, err := decodeSale(raw)
		if err != nil {
			t.Fatal(err)
		}
		assertSale(t, stored, winner)
		// Whoever won the race, every other attempt either got the original (same input) or the conflict.
		for _, r := range failures {
			if !errors.Is(r.err, out.ErrIdempotencyConflict) || r.sale != nil {
				t.Fatalf("losing attempt: sale=%v err=%v", r.sale, r.err)
			}
			if r.variant == winnerIsVariant {
				t.Fatal("an attempt with the winner's own input was rejected")
			}
		}
	})
}
