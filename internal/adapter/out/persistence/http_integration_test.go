package persistence

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"github.com/code-corhuila/synkro-sales-api/internal/adapter/in/httpapi"
	"github.com/code-corhuila/synkro-sales-api/internal/application/usecase"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type integrationClock struct{ date time.Time }

func (c integrationClock) Now() time.Time { return c.date }

type integrationIDs struct{}

func (integrationIDs) NewID() string { return uuid.NewString() }

func runHTTPIntegration(t *testing.T, client *mongo.Client) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2026, 10, 11, 0, 0, 0, 0, time.UTC)
	clock := integrationClock{day.Add(12 * time.Hour)}
	verifier, err := httpapi.NewJWTVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})), clock)
	if err != nil {
		t.Fatal(err)
	}
	collection := client.Database("sales").Collection("sale")
	repo := NewMongoRepository(collection)
	dependencies := func(client *mongo.Client, repo *MongoRepository) httpapi.Dependencies {
		return httpapi.Dependencies{Register: usecase.NewRegisterSale(repo, integrationIDs{}, clock), Queries: usecase.NewSalesQueries(repo), Reports: usecase.NewSalesReports(repo), Verifier: verifier, Health: func(ctx context.Context) error { return client.Ping(ctx, nil) }, Clock: clock, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Timeout: 2 * time.Second}
	}
	router := httpapi.NewRouter(dependencies(client, repo))
	token := func(sub, role, permission string) string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
		p, _ := json.Marshal(map[string]any{"sub": sub, "roles": []string{role}, "permissions": []string{permission}, "iat": clock.Now().Add(-time.Minute).Unix(), "exp": clock.Now().Add(time.Minute).Unix(), "jti": uuid.NewString()})
		unsigned := h + "." + base64.RawURLEncoding.EncodeToString(p)
		digest := sha256.Sum256([]byte(unsigned))
		sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	}
	owner, other, customer, product := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	idempotency := uuid.NewString() + ":register-sale"
	body := `{"customerId":"` + customer + `","createdBy":"` + owner + `","details":[{"productId":"` + product + `","quantity":2,"unitPriceCents":1250}]}`
	request := func(handler http.Handler, method, path, body, sub, role, permission string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token(sub, role, permission))
		r.Header.Set("Idempotency-Key", idempotency)
		r.Header.Set("X-Correlation-Id", "mongo-http-trace")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Header().Get("X-Correlation-Id") != "mongo-http-trace" {
			t.Fatal("correlation lost")
		}
		return w
	}
	var original httpapi.SaleResponse
	t.Run("FirstRegistration", func(t *testing.T) {
		w := request(router, "POST", "/api/v1/sales", body, "synkro-workflow", "SERVICE", "sales:register")
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &original) != nil || original.CreatedBy != owner || original.TotalCents != 2500 || w.Header().Get("Location") != "/api/v1/sales/"+original.SaleID {
			t.Fatal(w.Code, w.Body.String())
		}
		total, err := collection.CountDocuments(ctx, bson.D{{Key: "idempotencyKey", Value: idempotency}})
		if err != nil || total != 1 {
			t.Fatal("atomic HTTP insertion not observed", err)
		}
	})
	if original.SaleID == "" {
		t.Fatal("registration prerequisite failed")
	}
	t.Run("Replay", func(t *testing.T) {
		w := request(router, "POST", "/api/v1/sales", strings.ReplaceAll(body, "1250", "9000"), "synkro-workflow", "SERVICE", "sales:register")
		var got httpapi.SaleResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.SaleID != original.SaleID || got.TotalCents != original.TotalCents || got.Details[0].DetailID != original.Details[0].DetailID {
			t.Fatal(w.Code, w.Body.String())
		}
		total, err := collection.CountDocuments(ctx, bson.D{{Key: "idempotencyKey", Value: idempotency}})
		if err != nil || total != 1 {
			t.Fatal("replay inserted another document", err)
		}
	})
	t.Run("AdminListAndDetail", func(t *testing.T) {
		w := request(router, "GET", "/api/v1/sales?customerId="+customer, "", owner, "ADMIN", "sales:read")
		var list httpapi.ListSalesResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Meta.Total != 1 || len(list.Data) != 1 || list.Data[0].SaleID != original.SaleID {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request(router, "GET", "/api/v1/sales/"+original.SaleID, "", owner, "ADMIN", "sales:read")
		var got httpapi.SaleResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Details[0].UnitPriceCents != 1250 || got.Details[0].SubtotalCents != 2500 {
			t.Fatal(w.Code, w.Body.String())
		}
	})
	// Isolated customer lets pagination assertions ignore the original adapter suite.
	insert := func(owner string, date time.Time, active bool, inconsistent bool) string {
		t.Helper()
		s, err := model.RegisterSale(uuid.NewString(), customer, owner, date, []model.LineInput{{DetailID: uuid.NewString(), ProductID: product, Quantity: 1, UnitPriceCents: 100}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := encodeSale(uuid.NewString()+":http", s)
		if err != nil {
			t.Fatal(err)
		}
		var doc bson.M
		if err := bson.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		doc["active"] = active
		if inconsistent {
			doc["totalCents"] = int64(1)
		}
		if _, err := collection.InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
		return s.ID()
	}
	start := insert(owner, day, true, false)
	last := insert(owner, day.Add(24*time.Hour-time.Millisecond), true, false)
	foreign := insert(other, day, true, false)
	insert(owner, day.Add(-time.Millisecond), true, false)
	insert(owner, day.Add(24*time.Hour), true, false)
	inactive := insert(owner, day, false, false)
	t.Run("SalespersonOwnership", func(t *testing.T) {
		w := request(router, "GET", "/api/v1/sales?customerId="+customer+"&limit=1", "", owner, "SALESPERSON", "sales:read")
		var list httpapi.ListSalesResponse
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Meta.Total != 5 || list.Meta.TotalPages != 5 || len(list.Data) != 1 || list.Data[0].CreatedBy != owner {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request(router, "GET", "/api/v1/sales/"+original.SaleID, "", owner, "SALESPERSON", "sales:read")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	})
	t.Run("ForeignSaleForbidden", func(t *testing.T) {
		w := request(router, "GET", "/api/v1/sales/"+foreign, "", owner, "SALESPERSON", "sales:read")
		if w.Code != 403 || !strings.Contains(w.Body.String(), `"error":"FORBIDDEN"`) {
			t.Fatal(w.Code, w.Body.String())
		}
	})
	t.Run("UnknownAndInactiveNotFound", func(t *testing.T) {
		for _, id := range []string{uuid.NewString(), inactive} {
			w := request(router, "GET", "/api/v1/sales/"+id, "", owner, "ADMIN", "sales:read")
			if w.Code != 404 {
				t.Fatal(w.Code, w.Body.String())
			}
		}
	})
	t.Run("PaginationUTC", func(t *testing.T) {
		var ids []string
		for page := 1; page <= 5; page++ {
			path := "/api/v1/sales?customerId=" + customer + "&from=2026-10-11&to=2026-10-11&limit=1&page=" + string(rune('0'+page))
			w := request(router, "GET", path, "", owner, "ADMIN", "sales:read")
			var list httpapi.ListSalesResponse
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || list.Meta.Total != 4 || list.Meta.TotalPages != 4 {
				t.Fatal(w.Code, w.Body.String())
			}
			for _, s := range list.Data {
				ids = append(ids, s.SaleID)
			}
		}
		if len(ids) != 4 || ids[0] != last || ids[1] != original.SaleID {
			t.Fatal("day boundaries incorrect", ids)
		}
		if start > foreign {
			if ids[2] != start || ids[3] != foreign {
				t.Fatal("unstable ties", ids)
			}
		} else if ids[2] != foreign || ids[3] != start {
			t.Fatal("unstable ties", ids)
		}
		w := request(router, "GET", "/api/v1/sales?customerId="+customer+"&from=2026-10-11&to=2026-10-11", "", owner, "SALESPERSON", "sales:read")
		var list httpapi.ListSalesResponse
		_ = json.Unmarshal(w.Body.Bytes(), &list)
		if w.Code != 200 || list.Meta.Total != 3 {
			t.Fatal("ownership count includes other salesperson", w.Body.String())
		}
	})
	t.Run("InconsistentDocumentRejected", func(t *testing.T) {
		id := insert(owner, day, true, true)
		w := request(router, "GET", "/api/v1/sales/"+id, "", owner, "ADMIN", "sales:read")
		if w.Code != 500 || !strings.Contains(w.Body.String(), `"error":"INTERNAL_ERROR"`) {
			t.Fatal("inconsistent amounts exposed", w.Code, w.Body.String())
		}
		w = request(router, "GET", "/api/v1/sales?customerId="+customer, "", owner, "ADMIN", "sales:read")
		if w.Code != 500 {
			t.Fatal("list silently skipped corruption", w.Code, w.Body.String())
		}
		// A faithfully typed document can still overflow domain arithmetic.
		// Its restoration failure is a database error, including on replay.
		doc := validDocument(t)
		key := uuid.NewString() + ":corrupt-replay"
		doc["idempotencyKey"] = key
		doc["customerId"] = customer
		doc["createdBy"] = owner
		doc["totalCents"] = int64(1)
		firstDetail(doc)["quantity"] = int32(2)
		firstDetail(doc)["unitPriceCents"] = int64(9223372036854775807)
		firstDetail(doc)["subtotalCents"] = int64(1)
		if _, err := collection.InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
		w = request(router, "GET", "/api/v1/sales/"+doc["_id"].(string), "", owner, "ADMIN", "sales:read")
		if w.Code != 500 {
			t.Fatal("stored overflow was not internal", w.Code, w.Body.String())
		}
		originalKey := idempotency
		idempotency = key
		w = request(router, "POST", "/api/v1/sales", body, "synkro-workflow", "SERVICE", "sales:register")
		idempotency = originalKey
		if w.Code != 500 || !strings.Contains(w.Body.String(), `"error":"INTERNAL_ERROR"`) {
			t.Fatal("unsafe replay exposed as request invariant", w.Code, w.Body.String())
		}
	})
	t.Run("DatabaseFailure", func(t *testing.T) {
		unavailable := integrationClient(t, "mongodb://127.0.0.1:1/sales?directConnection=true")
		broken := httpapi.NewRouter(dependencies(unavailable, NewMongoRepository(unavailable.Database("sales").Collection("sale"))))
		for _, path := range []string{"/api/v1/sales", "/api/v1/sales/" + original.SaleID} {
			w := request(broken, "GET", path, "", owner, "ADMIN", "sales:read")
			if w.Code != 500 || strings.Contains(w.Body.String(), "127.0.0.1") {
				t.Fatal(w.Code, w.Body.String())
			}
		}
		w := request(broken, "POST", "/api/v1/sales", body, "synkro-workflow", "SERVICE", "sales:register")
		if w.Code != 500 {
			t.Fatal(w.Code, w.Body.String())
		}
		w = request(broken, "GET", "/health", "", owner, "ADMIN", "sales:read")
		if w.Code != 503 {
			t.Fatal(w.Code, w.Body.String())
		}
	})
	runReportHTTPIntegration(t, collection, router, request, func() http.Handler {
		unavailable := integrationClient(t, "mongodb://127.0.0.1:1/sales?directConnection=true")
		return httpapi.NewRouter(dependencies(unavailable, NewMongoRepository(unavailable.Database("sales").Collection("sale"))))
	})
	t.Run("NoDeletePermission", func(t *testing.T) {
		_, err := collection.DeleteOne(ctx, bson.D{{Key: "_id", Value: original.SaleID}})
		var command mongo.CommandError
		if !errors.As(err, &command) || command.Code != 13 {
			t.Fatal("sales_app deletion not denied", err)
		}
		sale, err := repo.FindActiveByID(ctx, original.SaleID)
		if err != nil || sale.ID() != original.SaleID {
			t.Fatal("denied deletion altered sale", err)
		}
	})
}
