package persistence

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func integrationClient(t *testing.T, uri string) *mongo.Client {
	t.Helper()
	client, err := mongo.Connect(options.Client().ApplyURI(uri).SetServerSelectionTimeout(2 * time.Second))
	if err != nil {
		t.Fatal("cannot configure integration client")
	}
	t.Cleanup(func() {
		if err := client.Disconnect(context.Background()); err != nil {
			t.Error("disconnect failed")
		}
	})
	return client
}

func TestMongoIntegration(t *testing.T) {
	uri := os.Getenv("TEST_MONGODB_URI")
	if uri == "" {
		if os.Getenv("REQUIRE_MONGODB_INTEGRATION") == "1" {
			t.Fatal("mandatory integration URI missing")
		}
		t.Skip("TEST_MONGODB_URI is unset")
	}
	u, err := url.Parse(uri)
	// The launcher verifies the label and tmpfs before issuing this attestation.
	// Permission probes must never target an ordinary developer database.
	host := os.Getenv("TEST_MONGO_HOST")
	if err != nil || u.User == nil || u.User.Username() != "sales_app" || u.Path != "/sales" || u.Host != host+":27017" || !strings.HasPrefix(host, "synkro-sales-api-it-") || u.Query().Get("replicaSet") != "rs0" || u.Query().Get("authSource") != "admin" || os.Getenv("TEST_MONGODB_DISPOSABLE") != "1" {
		t.Fatal("integration requires the launcher's disposable sales_app replica set")
	}
	client := integrationClient(t, uri)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := client.Ping(ctx, nil); err != nil {
		t.Fatal("sales_app connection failed")
	}
	var status struct {
		AuthInfo struct {
			Users []struct {
				User string `bson:"user"`
				DB   string `bson:"db"`
			} `bson:"authenticatedUsers"`
		} `bson:"authInfo"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}}).Decode(&status); err != nil {
		t.Fatal(err)
	}
	if len(status.AuthInfo.Users) != 1 || status.AuthInfo.Users[0].User != "sales_app" || status.AuthInfo.Users[0].DB != "admin" {
		t.Fatal("connection is not authenticated solely as sales_app")
	}
	var hello struct {
		Primary bool   `bson:"isWritablePrimary"`
		Set     string `bson:"setName"`
		Me      string `bson:"me"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		t.Fatal(err)
	}
	if !hello.Primary || hello.Set != "rs0" || hello.Me != host+":27017" {
		t.Fatal("test runner did not resolve the writable replica set member")
	}
	var build struct {
		Version string `bson:"version"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&build); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(build.Version, "7.0.") {
		t.Fatalf("MongoDB version=%s, want 7.0", build.Version)
	}
	collection := client.Database("sales").Collection("sale")
	repo := NewMongoRepository(collection, client.Database("sales").Collection("sale_counter"))
	t.Run("HTTP", func(t *testing.T) { runHTTPIntegration(t, client) })
	t.Run("Reports", func(t *testing.T) { runReportIntegration(t, client) })
	newKey := func() string { return uuid.NewString() + ":register-sale" }
	countOne := func(t *testing.T, key string) {
		t.Helper()
		count, err := collection.CountDocuments(ctx, bson.M{"idempotencyKey": key})
		if err != nil || count != 1 {
			t.Fatalf("document count=%d err=%v", count, err)
		}
	}

	t.Run("FirstInsertion", func(t *testing.T) {
		s, key := saleFixture(t, 100), newKey()
		stored, created, err := repo.CreateOnce(ctx, key, s)
		if err != nil || !created {
			t.Fatalf("first insertion: created=%v err=%v", created, err)
		}
		assertSale(t, stored, s)
		countOne(t, key)
		raw, err := collection.FindOne(ctx, bson.M{"idempotencyKey": key}).Raw()
		if err != nil {
			t.Fatal(err)
		}
		assertSaleBSON(t, raw, s, key)
	})
	t.Run("IdempotentReplay", func(t *testing.T) {
		s, key := saleFixture(t, 1), newKey()
		first, created, err := repo.CreateOnce(ctx, key, s)
		if err != nil || !created {
			t.Fatal("first insertion failed", err)
		}
		attempt := saleFixture(t, 2)
		got, created, err := repo.CreateOnce(ctx, key, attempt)
		if err != nil || created {
			t.Fatalf("replay: created=%v err=%v", created, err)
		}
		assertSale(t, got, first)
		countOne(t, key)
	})
	t.Run("DuplicateIDDifferentKey", func(t *testing.T) {
		s, key := saleFixture(t, 1), newKey()
		if _, _, err := repo.CreateOnce(ctx, key, s); err != nil {
			t.Fatal(err)
		}
		other := newKey()
		got, created, err := repo.CreateOnce(ctx, other, s)
		if err == nil || !mongo.IsDuplicateKeyError(err) || got != nil || created {
			t.Fatal("unexpected duplicate _id treated as success", err)
		}
		countOne(t, key)
		count, err := collection.CountDocuments(ctx, bson.M{"idempotencyKey": other})
		if err != nil || count != 0 {
			t.Fatal("different key was inserted")
		}
	})
	t.Run("ConcurrentRegistration", func(t *testing.T) {
		const n = 32
		key := newKey()
		fixtures := make([]*model.Sale, n)
		for i := range fixtures {
			fixtures[i] = saleFixture(t, 1)
		}
		type outcome struct {
			sale    *model.Sale
			created bool
			err     error
		}
		results := make(chan outcome, n)
		start := make(chan struct{})
		for _, fixture := range fixtures {
			go func(s *model.Sale) {
				<-start
				got, created, err := repo.CreateOnce(ctx, key, s)
				results <- outcome{got, created, err}
			}(fixture)
		}
		close(start)
		var original *model.Sale
		created := 0
		for i := 0; i < n; i++ {
			got := <-results
			if got.err != nil || got.sale == nil {
				t.Fatalf("concurrent operation failed: %v", got.err)
			}
			if original == nil {
				original = got.sale
			}
			assertSale(t, got.sale, original)
			if got.created {
				created++
			}
		}
		if created != 1 {
			t.Fatalf("created=%d, want 1", created)
		}
		countOne(t, key)
		t.Log("32 operations returned one original sale; exactly one creation and one document")
	})
	t.Run("CommercialNumberConcurrency", func(t *testing.T) {
		const n = 32
		type outcome struct {
			sale *model.Sale
			err  error
		}
		results := make(chan outcome, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			go func(s *model.Sale, key string) {
				<-start
				got, created, err := repo.CreateOnce(ctx, key, s)
				if err == nil && !created {
					err = errors.New("distinct key reported as replay")
				}
				results <- outcome{got, err}
			}(saleFixture(t, 1), newKey())
		}
		close(start)
		seen := map[string]bool{}
		for i := 0; i < n; i++ {
			got := <-results
			if got.err != nil {
				t.Fatalf("concurrent registration failed: %v", got.err)
			}
			number := got.sale.CommercialNumber()
			if !model.ValidCommercialNumber(number) || seen[number] {
				t.Fatalf("number %q is invalid or repeated", number)
			}
			seen[number] = true
		}
		t.Logf("%d concurrent distinct sales received %d distinct commercial numbers", n, len(seen))
	})
	t.Run("CommercialNumberReplay", func(t *testing.T) {
		key := newKey()
		first, created, err := repo.CreateOnce(ctx, key, saleFixture(t, 1))
		if err != nil || !created || first.CommercialNumber() == "" {
			t.Fatalf("first: created=%v number=%q err=%v", created, first.CommercialNumber(), err)
		}
		var counterBefore struct {
			Seq int64 `bson:"seq"`
		}
		counters := client.Database("sales").Collection("sale_counter")
		year := fmt.Sprintf("VEN-%04d", model.CommercialYear(first.Date()))
		if err := counters.FindOne(ctx, bson.M{"_id": year}).Decode(&counterBefore); err != nil {
			t.Fatal(err)
		}
		again, created, err := repo.CreateOnce(ctx, key, saleFixture(t, 2))
		if err != nil || created {
			t.Fatalf("replay: created=%v err=%v", created, err)
		}
		if again.CommercialNumber() != first.CommercialNumber() || again.ID() != first.ID() {
			t.Fatalf("replay changed the sale: %q vs %q", again.CommercialNumber(), first.CommercialNumber())
		}
		var counterAfter struct {
			Seq int64 `bson:"seq"`
		}
		if err := counters.FindOne(ctx, bson.M{"_id": year}).Decode(&counterAfter); err != nil {
			t.Fatal(err)
		}
		if counterAfter.Seq != counterBefore.Seq {
			t.Fatalf("a replay consumed a number: %d -> %d", counterBefore.Seq, counterAfter.Seq)
		}
	})
	t.Run("CommercialNumberExhaustion", func(t *testing.T) {
		counters := client.Database("sales").Collection("sale_counter")
		if _, err := counters.UpdateOne(ctx, bson.M{"_id": "VEN-2099"}, bson.M{"$set": bson.M{"seq": int64(999999)}}, options.UpdateOne().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
		late, err := model.RegisterSale(uuid.NewString(), uuid.NewString(), uuid.NewString(), time.Date(2099, 6, 1, 12, 0, 0, 0, time.UTC),
			[]model.LineInput{{DetailID: uuid.NewString(), ProductID: uuid.NewString(), Quantity: 1, UnitPriceCents: 100}})
		if err != nil {
			t.Fatal(err)
		}
		key := newKey()
		if _, _, err := repo.CreateOnce(ctx, key, late); !errors.Is(err, model.ErrCommercialSequenceExhausted) {
			t.Fatalf("exhaustion must be an explicit error, got %v", err)
		}
		if count, err := collection.CountDocuments(ctx, bson.M{"idempotencyKey": key}); err != nil || count != 0 {
			t.Fatalf("a sale without a number was stored: count=%d err=%v", count, err)
		}
	})
	// Controlled instants (far-future years, so no other scenario shares their counters).
	cot := time.FixedZone("COT", -5*60*60)
	counters := client.Database("sales").Collection("sale_counter")
	saleAt := func(t *testing.T, id string, at time.Time) *model.Sale {
		t.Helper()
		s, err := model.RegisterSale(id, uuid.NewString(), uuid.NewString(), at,
			[]model.LineInput{{DetailID: uuid.NewString(), ProductID: uuid.NewString(), Quantity: 1, UnitPriceCents: 100}})
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	counterSeq := func(t *testing.T, id string) int64 {
		t.Helper()
		var c struct {
			Seq int64 `bson:"seq"`
		}
		err := counters.FindOne(ctx, bson.M{"_id": id}).Decode(&c)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return 0
		}
		if err != nil {
			t.Fatal(err)
		}
		return c.Seq
	}
	t.Run("CommercialNumberYearRollover", func(t *testing.T) {
		keyA := newKey()
		steps := []struct {
			name string
			at   time.Time
			key  string
			want string
		}{
			{"last second of 2090 (Colombia)", time.Date(2090, 12, 31, 23, 59, 59, 0, cot), keyA, "VEN-2090-000001"},
			{"first instant of 2091 (Colombia)", time.Date(2091, 1, 1, 0, 0, 0, 0, cot), newKey(), "VEN-2091-000001"},
			{"2090 sequence continues after 2091 started", time.Date(2090, 12, 31, 23, 59, 59, 999_000_000, cot), newKey(), "VEN-2090-000002"},
			{"04:59:59 UTC on 1 Jan is still 2090 in Colombia", time.Date(2091, 1, 1, 4, 59, 59, 0, time.UTC), newKey(), "VEN-2090-000003"},
			{"05:00:00 UTC on 1 Jan is already 2091 in Colombia", time.Date(2091, 1, 1, 5, 0, 0, 0, time.UTC), newKey(), "VEN-2091-000002"},
		}
		for _, step := range steps {
			got, created, err := repo.CreateOnce(ctx, step.key, saleAt(t, uuid.NewString(), step.at))
			if err != nil || !created || got.CommercialNumber() != step.want {
				t.Fatalf("%s: created=%v err=%v, want %s", step.name, created, err, step.want)
			}
			raw, err := collection.FindOne(ctx, bson.M{"idempotencyKey": step.key}).Raw()
			if err != nil || raw.Lookup("commercialNumber").StringValue() != step.want {
				t.Fatalf("%s: stored number differs from the response (%v)", step.name, err)
			}
		}
		// A retry of the first request after the year changed returns the original number and spends none.
		before := counterSeq(t, "VEN-2091")
		again, created, err := repo.CreateOnce(ctx, keyA, saleAt(t, uuid.NewString(), time.Date(2091, 1, 2, 9, 0, 0, 0, cot)))
		if err != nil || created || again.CommercialNumber() != "VEN-2090-000001" {
			t.Fatalf("replay across the year change: created=%v err=%v", created, err)
		}
		if after := counterSeq(t, "VEN-2091"); after != before || counterSeq(t, "VEN-2090") != 3 {
			t.Fatalf("replay changed a counter: 2091 %d -> %d, 2090 = %d", before, after, counterSeq(t, "VEN-2090"))
		}
	})
	t.Run("CommercialNumberSameKeyConcurrent", func(t *testing.T) {
		const n = 16
		key, at := newKey(), time.Date(2093, 3, 1, 10, 0, 0, 0, cot)
		before := counterSeq(t, "VEN-2093")
		type outcome struct {
			number  string
			created bool
			err     error
		}
		results := make(chan outcome, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			go func(s *model.Sale) {
				<-start
				got, created, err := repo.CreateOnce(ctx, key, s)
				if err != nil {
					results <- outcome{err: err}
					return
				}
				results <- outcome{got.CommercialNumber(), created, nil}
			}(saleAt(t, uuid.NewString(), at))
		}
		close(start)
		numbers, created := map[string]int{}, 0
		for i := 0; i < n; i++ {
			got := <-results
			if got.err != nil {
				t.Fatalf("concurrent request failed: %v", got.err)
			}
			numbers[got.number]++
			if got.created {
				created++
			}
		}
		countOne(t, key)
		raw, err := collection.FindOne(ctx, bson.M{"idempotencyKey": key}).Raw()
		if err != nil {
			t.Fatal(err)
		}
		stored := raw.Lookup("commercialNumber").StringValue()
		if created != 1 || len(numbers) != 1 || numbers[stored] != n {
			t.Fatalf("created=%d numbers=%v stored=%s: one sale must keep one stable number", created, numbers, stored)
		}
		// Losers of the race may each have spent a number before their insert lost: that is the gap.
		spent := counterSeq(t, "VEN-2093") - before
		if spent < 1 || spent > n {
			t.Fatalf("spent %d numbers for one sale", spent)
		}
		t.Logf("%d concurrent requests with one key: 1 sale, number %s, %d counter values spent", n, stored, spent)
	})
	t.Run("CommercialNumberGapWhenInsertFails", func(t *testing.T) {
		at, id := time.Date(2094, 6, 1, 12, 0, 0, 0, cot), uuid.NewString()
		first, _, err := repo.CreateOnce(ctx, newKey(), saleAt(t, id, at))
		if err != nil || first.CommercialNumber() != "VEN-2094-000001" {
			t.Fatalf("first sale: %v", err)
		}
		// The counter is advanced before the insert and they are not one transaction: an insert that
		// fails after the reservation (here, a reused sale id) leaves the number spent and unused.
		lost := newKey()
		if _, _, err := repo.CreateOnce(ctx, lost, saleAt(t, id, at)); err == nil || !mongo.IsDuplicateKeyError(err) {
			t.Fatalf("expected a duplicate key failure, got %v", err)
		}
		if count, err := collection.CountDocuments(ctx, bson.M{"idempotencyKey": lost}); err != nil || count != 0 {
			t.Fatalf("a failed insert left a sale behind: count=%d err=%v", count, err)
		}
		if seq := counterSeq(t, "VEN-2094"); seq != 2 {
			t.Fatalf("counter = %d, want 2 (one number spent without a sale)", seq)
		}
		next, _, err := repo.CreateOnce(ctx, newKey(), saleAt(t, uuid.NewString(), at))
		if err != nil || next.CommercialNumber() != "VEN-2094-000003" {
			t.Fatalf("the next sale must skip the spent number, got %q (%v)", next.CommercialNumber(), err)
		}
		// Retrying the failed request allocates a fresh number: only a stored sale owns one.
		retry, created, err := repo.CreateOnce(ctx, lost, saleAt(t, uuid.NewString(), at))
		if err != nil || !created || retry.CommercialNumber() != "VEN-2094-000004" {
			t.Fatalf("retry of a failed request: %q created=%v err=%v", retry.CommercialNumber(), created, err)
		}
		again, created, err := repo.CreateOnce(ctx, lost, saleAt(t, uuid.NewString(), at))
		if err != nil || created || again.CommercialNumber() != retry.CommercialNumber() {
			t.Fatalf("an uncertain retry after the insert must return the stored number: %q created=%v err=%v", again.CommercialNumber(), created, err)
		}
	})
	t.Run("CommercialNumberExhaustionLeavesNoPartialSale", func(t *testing.T) {
		if _, err := counters.UpdateOne(ctx, bson.M{"_id": "VEN-2098"}, bson.M{"$set": bson.M{"seq": int64(999998)}}, options.UpdateOne().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
		at := time.Date(2098, 5, 1, 12, 0, 0, 0, cot)
		lastKey := newKey()
		last, _, err := repo.CreateOnce(ctx, lastKey, saleAt(t, uuid.NewString(), at))
		if err != nil || last.CommercialNumber() != "VEN-2098-999999" {
			t.Fatalf("the last number of the year must be issued: %v", err)
		}
		key := newKey()
		if got, created, err := repo.CreateOnce(ctx, key, saleAt(t, uuid.NewString(), at)); !errors.Is(err, model.ErrCommercialSequenceExhausted) || created || got != nil {
			t.Fatalf("number 1000000 must fail explicitly, got %v", err)
		}
		if count, err := collection.CountDocuments(ctx, bson.M{"idempotencyKey": key}); err != nil || count != 0 {
			t.Fatalf("a partial sale was stored: count=%d err=%v", count, err)
		}
		if seq := counterSeq(t, "VEN-2098"); seq != 999999 {
			t.Fatalf("the counter moved past the ceiling: %d", seq)
		}
		// The last sale of the year can still be replayed at the ceiling.
		if again, created, err := repo.CreateOnce(ctx, lastKey, saleAt(t, uuid.NewString(), at)); err != nil || created || again.CommercialNumber() != "VEN-2098-999999" {
			t.Fatalf("replay at the ceiling: %v", err)
		}
	})
	t.Run("ValidatorRejection", func(t *testing.T) {
		cases := map[string]func(bson.M){
			"UndeclaredField": func(d bson.M) { d["status"] = "PAID" },
			"InvalidUUID":     func(d bson.M) { d["_id"] = "id-1" },
			"MoneyInt32":      func(d bson.M) { d["totalCents"] = int32(2500) },
			"QuantityInt64":   func(d bson.M) { firstDetail(d)["quantity"] = int64(2) },
			"101Lines": func(d bson.M) {
				a := bson.A{}
				for i := 0; i < 101; i++ {
					a = append(a, firstDetail(d))
				}
				d["details"] = a
			},
		}
		for _, field := range []string{"_id", "customerId", "createdBy", "date", "totalCents", "active", "idempotencyKey", "details"} {
			field := field
			cases["Missing_"+field] = func(d bson.M) { delete(d, field) }
		}
		for _, field := range []string{"detailId", "productId", "quantity", "unitPriceCents", "subtotalCents", "active"} {
			field := field
			cases["MissingDetail_"+field] = func(d bson.M) { delete(firstDetail(d), field) }
		}
		for name, mutate := range cases {
			t.Run(name, func(t *testing.T) {
				d := validDocument(t)
				d["idempotencyKey"] = newKey()
				mutate(d)
				_, err := collection.InsertOne(ctx, d)
				var exception mongo.WriteException
				if !errors.As(err, &exception) || !exception.HasErrorCode(121) {
					t.Fatalf("expected validator error 121, got %v", err)
				}
			})
		}
	})
	t.Run("Permissions", func(t *testing.T) {
		s, key := saleFixture(t, 1), newKey()
		if _, _, err := repo.CreateOnce(ctx, key, s); err != nil {
			t.Fatal("sales_app insert failed", err)
		}
		countOne(t, key)
		_, err := collection.DeleteOne(ctx, bson.M{"_id": s.ID()})
		var command mongo.CommandError
		if !errors.As(err, &command) || command.Code != 13 {
			t.Fatalf("expected remove authorization error 13, got %v", err)
		}
		countOne(t, key)
	})
	t.Run("UnsafeReplayRejected", func(t *testing.T) {
		for _, field := range []string{"active", "detailActive", "totalCents", "subtotalCents"} {
			t.Run(field, func(t *testing.T) {
				d, key := validDocument(t), newKey()
				d["idempotencyKey"] = key
				switch field {
				case "active":
					d["active"] = false
				case "detailActive":
					firstDetail(d)["active"] = false
				case "totalCents":
					d["totalCents"] = int64(1)
				case "subtotalCents":
					firstDetail(d)["subtotalCents"] = int64(1)
				}
				if _, err := collection.InsertOne(ctx, d); err != nil {
					t.Fatal(err)
				}
				got, created, err := repo.CreateOnce(ctx, key, saleFixture(t, 1))
				if !errors.Is(err, ErrInvalidSaleDocument) || got != nil || created {
					t.Fatal("unsafe replay reported as successful", err)
				}
				countOne(t, key)
			})
		}
	})
	t.Run("CancelledContext", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		got, created, err := repo.CreateOnce(cancelled, newKey(), saleFixture(t, 1))
		if !errors.Is(err, context.Canceled) || got != nil || created {
			t.Fatalf("cancelled insertion reported success: %v", err)
		}
	})
	t.Run("InvalidAuthentication", func(t *testing.T) {
		bad := *u
		bad.User = url.UserPassword("sales_app", uuid.NewString())
		invalid := integrationClient(t, bad.String())
		got, created, err := NewMongoRepository(invalid.Database("sales").Collection("sale"), invalid.Database("sales").Collection("sale_counter")).CreateOnce(ctx, newKey(), saleFixture(t, 1))
		if err == nil || got != nil || created {
			t.Fatal("invalid credentials reported success")
		}
		var serverError mongo.ServerError
		if !errors.As(err, &serverError) || !serverError.HasErrorCode(18) {
			t.Fatalf("failure was not authentication error 18: %v", err)
		}
	})
	t.Run("UnavailableServer", func(t *testing.T) {
		unavailable := integrationClient(t, "mongodb://127.0.0.1:1/sales?directConnection=true")
		got, created, err := NewMongoRepository(unavailable.Database("sales").Collection("sale"), unavailable.Database("sales").Collection("sale_counter")).CreateOnce(ctx, newKey(), saleFixture(t, 1))
		if err == nil || got != nil || created {
			t.Fatal("unavailable server reported success")
		}
	})
}
