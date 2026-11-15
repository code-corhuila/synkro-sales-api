package persistence

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/adapter/in/httpapi"
	"github.com/code-corhuila/synkro-sales-api/internal/application/usecase"
	"github.com/code-corhuila/synkro-sales-api/internal/domain/model"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// lockedBuffer collects structured logs from concurrent requests.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *lockedBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// securityHarness runs the real router and use case over the real, migrated MongoDB, with
// tokens signed by a key the router trusts. It is shared by the security regressions of
// issues 284, 285 and 286; every assertion on persistence counts documents in the database.
type securityHarness struct {
	t          *testing.T
	ctx        context.Context
	collection *mongo.Collection
	key        *rsa.PrivateKey
	clock      integrationClock
	router     http.Handler
	logs       *lockedBuffer
}

func newSecurityHarness(t *testing.T, client *mongo.Client) *securityHarness {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	clock := integrationClock{time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)}
	verifier, err := httpapi.NewJWTVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})), clock)
	if err != nil {
		t.Fatal(err)
	}
	database := client.Database("sales")
	repo := NewMongoRepository(database.Collection("sale"), database.Collection("sale_counter"))
	logs := &lockedBuffer{}
	router := httpapi.NewRouter(httpapi.Dependencies{
		Register: usecase.NewRegisterSale(repo, integrationIDs{}, clock), Queries: usecase.NewSalesQueries(repo), Reports: usecase.NewSalesReports(repo),
		Verifier: verifier, Health: func(ctx context.Context) error { return client.Ping(ctx, nil) },
		Clock: clock, Logger: slog.New(slog.NewJSONHandler(logs, nil)), Timeout: 2 * time.Second,
	})
	return &securityHarness{t: t, ctx: ctx, collection: database.Collection("sale"), key: key, clock: clock, router: router, logs: logs}
}

func (h *securityHarness) claims(sub, role string, permissions ...string) map[string]any {
	if permissions == nil {
		permissions = []string{}
	}
	now := h.clock.Now()
	return map[string]any{"sub": sub, "roles": []string{role}, "permissions": permissions, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(), "jti": uuid.NewString()}
}

func (h *securityHarness) sign(key *rsa.PrivateKey, algorithm string, claims map[string]any) string {
	h.t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"` + algorithm + `"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		h.t.Fatal(err)
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		h.t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// token is a correctly signed, unexpired token for the given principal.
func (h *securityHarness) token(sub, role string, permissions ...string) string {
	h.t.Helper()
	return h.sign(h.key, "RS256", h.claims(sub, role, permissions...))
}

// workflowToken is the only credential allowed to register sales.
func (h *securityHarness) workflowToken() string {
	h.t.Helper()
	return h.token("synkro-workflow", "SERVICE", "sales:register")
}

// post sends a registration. extra headers are applied last, so they can replace the Authorization header.
func (h *securityHarness) post(token, idempotencyKey, body string, extra map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest("POST", "/api/v1/sales", strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	r.Header.Set("Idempotency-Key", idempotencyKey)
	for name, value := range extra {
		r.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	h.router.ServeHTTP(w, r)
	return w
}

func (h *securityHarness) total() int64 {
	h.t.Helper()
	n, err := h.collection.CountDocuments(h.ctx, bson.D{})
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *securityHarness) countKey(key string) int64 {
	h.t.Helper()
	n, err := h.collection.CountDocuments(h.ctx, bson.D{{Key: "idempotencyKey", Value: key}})
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *securityHarness) stored(key string) *model.Sale {
	h.t.Helper()
	raw, err := h.collection.FindOne(h.ctx, bson.D{{Key: "idempotencyKey", Value: key}}).Raw()
	if err != nil {
		h.t.Fatal(err)
	}
	sale, err := decodeSale(raw)
	if err != nil {
		h.t.Fatal(err)
	}
	return sale
}

// sagaKey is the idempotency key the Workflow derives for its register-sale step.
func sagaKey() string { return uuid.NewString() + ":register-sale" }

func saleBody(customer, author, product string, quantity int, unitPriceCents int64) string {
	return fmt.Sprintf(`{"customerId":%q,"createdBy":%q,"details":[{"productId":%q,"quantity":%d,"unitPriceCents":%d}]}`, customer, author, product, quantity, unitPriceCents)
}

func decodeLogLines(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("log line is not JSON: %v", err)
		}
		records = append(records, record)
	}
	return records
}
