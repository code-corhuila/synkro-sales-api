package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/code-corhuila/synkro-sales-api/internal/application/port/in"
)

func TestAuthenticationRejectsDuplicateHeaders(t *testing.T) {
	f := newHTTPFixture(t)
	r := httptest.NewRequest("GET", "/api/v1/sales", nil)
	r.Header.Add("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, ownerID, "ADMIN", "sales:read")))
	r.Header.Add("Authorization", "Bearer forged")
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	assertHTTPError(t, w, 401, "UNAUTHORIZED")
	if f.repo.calls != 0 {
		t.Fatal("ambiguous credentials reached persistence")
	}
}
func TestProperHS256TokenIsRejected(t *testing.T) {
	f := newHTTPFixture(t)
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256"}`))
	claims, _ := json.Marshal(claimsAt(f.now, ownerID, "ADMIN", "sales:read"))
	unsigned := h + "." + base64.RawURLEncoding.EncodeToString(claims)
	key, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(unsigned))
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	r := httptest.NewRequest("GET", "/api/v1/sales", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	assertHTTPError(t, w, 401, "UNAUTHORIZED")
}
func TestQuantityStorageInvariantIsBusinessFailure(t *testing.T) {
	f := newHTTPFixture(t)
	w := f.request(t, "POST", "/api/v1/sales", strings.ReplaceAll(requestBody, `"quantity":2`, `"quantity":2147483648`), "synkro-workflow", "SERVICE", "sales:register")
	assertHTTPError(t, w, 422, "BUSINESS_RULE_VIOLATION")
	if f.repo.calls != 0 {
		t.Fatal("unrepresentable quantity persisted")
	}
}

type panicRegistration struct{}

func (panicRegistration) Register(context.Context, in.RegisterSaleCommand) (in.RegisterSaleResult, error) {
	panic("secret-database internal panic")
}
func TestUnexpectedFailureEnvelopeAndJSONLogging(t *testing.T) {
	f := newHTTPFixture(t)
	raw, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	verifier, err := NewJWTVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})), fixedClock{f.now})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	f.router = NewRouter(Dependencies{Register: panicRegistration{}, Verifier: verifier, Clock: fixedClock{f.now}, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	w := f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register")
	assertHTTPError(t, w, 500, "INTERNAL_ERROR")
	decoder := json.NewDecoder(&logs)
	for i := 0; i < 2; i++ {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record["correlationId"] != "contract-trace" || record["service"] != "synkro-sales-api" {
			t.Fatal("uncorrelated structured log", record)
		}
	}
}

func TestMissingDependenciesFailClosed(t *testing.T) {
	f := newHTTPFixture(t)
	raw, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	verifier, err := NewJWTVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})), fixedClock{f.now})
	if err != nil {
		t.Fatal(err)
	}
	f.router = NewRouter(Dependencies{Verifier: verifier, Clock: fixedClock{f.now}})
	assertHTTPError(t, f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register"), 500, "INTERNAL_ERROR")
	for _, path := range []string{"/api/v1/sales", "/api/v1/sales/" + ownerID} {
		assertHTTPError(t, f.request(t, "GET", path, "", ownerID, "ADMIN", "sales:read"), 500, "INTERNAL_ERROR")
	}
	w := f.request(t, "GET", "/health", "", "", "")
	if w.Code != 503 {
		t.Fatal("missing health dependency reported healthy")
	}
}
