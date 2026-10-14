package persistence

import (
	"context"
	"errors"
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
	repo := NewMongoRepository(collection)
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
		got, created, err := NewMongoRepository(invalid.Database("sales").Collection("sale")).CreateOnce(ctx, newKey(), saleFixture(t, 1))
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
		got, created, err := NewMongoRepository(unavailable.Database("sales").Collection("sale")).CreateOnce(ctx, newKey(), saleFixture(t, 1))
		if err == nil || got != nil || created {
			t.Fatal("unavailable server reported success")
		}
	})
}
