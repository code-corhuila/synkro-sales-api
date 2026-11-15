package httpapi

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/code-corhuila/synkro-sales-api/internal/application/usecase"
)

func TestCorrelationOrNewPreservesOpaqueContractValues(t *testing.T) {
	for _, trace := range []string{"contract-trace", "7c1d4e52-3a6b-4f08-9c21-5d8e0b1a2f34", "REQUEST-42"} {
		if got := correlationOrNew(trace); got != trace {
			t.Fatalf("correlation changed: %q", got)
		}
	}
	if first, second := correlationOrNew(""), correlationOrNew(""); !canonicalID(first) || first == second {
		t.Fatal("missing correlation must generate a unique UUID")
	}
}

func TestParseSagaKeyAcceptsOnlyUUIDAndKnownStep(t *testing.T) {
	const saga = "550e8400-e29b-41d4-a716-446655440000"
	for _, step := range []string{"validate-customer", "reserve-stock", "register-sale", "release-stock"} {
		id, got, ok := parseSagaKey(saga + ":" + step)
		if !ok || id != saga || got != step {
			t.Fatalf("%s was not parsed: %q %q %v", step, id, got, ok)
		}
	}
	for _, key := range []string{
		"", "saga-key:register-sale", saga, saga + ":", saga + ":unknown-step", saga + ":register-sale:extra",
		strings.ToUpper(saga) + ":register-sale", "TOKEN-MARKER:register-sale", saga + ":" + strings.Repeat("s", 500),
	} {
		if id, step, ok := parseSagaKey(key); ok || id != "" || step != "" {
			t.Fatalf("%q must not yield saga fields: %q %q", key, id, step)
		}
	}
}

func TestSafeActorAdmitsOnlyUUIDsAndApprovedServices(t *testing.T) {
	for _, ok := range []string{ownerID, "synkro-workflow", "synkro-worker"} {
		if safeActor(ok) != ok {
			t.Fatalf("%q should be a safe actor", ok)
		}
	}
	for _, bad := range []string{"", "alice@example.com", "Bearer abc", "synkro-other", strings.ToUpper(ownerID)} {
		if safeActor(bad) != "" {
			t.Fatalf("%q must not be logged as an actor", bad)
		}
	}
}

// Unit-level evidence over an in-memory repository; the real-database equivalent runs in the
// MongoDB integration suite.
func TestRegistrationAuditUsesOnlyAllowlistedFields(t *testing.T) {
	f := newHTTPFixture(t)
	raw, _ := x509.MarshalPKIXPublicKey(&f.key.PublicKey)
	verifier, err := NewJWTVerifier(string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw})), fixedClock{f.now})
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	f.router = NewRouter(Dependencies{Register: usecase.NewRegisterSale(f.repo, randomIDs{}, fixedClock{f.now}), Verifier: verifier, Clock: fixedClock{f.now}, Logger: slog.New(slog.NewJSONHandler(&logs, nil)), Timeout: time.Second})

	const marker = "TOKEN-MARKER-0d1f"
	hostile := httptest.NewRequest("POST", "/api/v1/sales", strings.NewReader(strings.Replace(requestBody, `"details"`, `"customerName":"Synthetic Name","password":"pw-`+marker+`","details"`, 1)))
	hostile.Header.Set("Authorization", "Bearer "+signToken(t, f.key, "RS256", claimsAt(f.now, "synkro-workflow", "SERVICE", "sales:register")))
	hostile.Header.Set("Idempotency-Key", marker+"-key-value")
	hostile.Header.Set("X-Correlation-Id", "audit-request")
	f.router.ServeHTTP(httptest.NewRecorder(), hostile)
	w := f.request(t, "POST", "/api/v1/sales", requestBody, "synkro-workflow", "SERVICE", "sales:register")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}

	allowed := map[string]bool{"time": true, "level": true, "msg": true, "service": true, "correlationId": true, "sagaId": true, "step": true, "actorId": true, "authorId": true, "action": true, "result": true}
	var results []string
	decoder := json.NewDecoder(strings.NewReader(logs.String()))
	for decoder.More() {
		var record map[string]any
		if err := decoder.Decode(&record); err != nil {
			t.Fatal(err)
		}
		if record["msg"] != "audit" {
			continue
		}
		for field := range record {
			if !allowed[field] {
				t.Fatalf("audit field outside the allowlist: %q", field)
			}
		}
		results = append(results, record["result"].(string))
	}
	if len(results) != 2 || results[0] != "rejected" || results[1] != "created" {
		t.Fatalf("audit results = %v", results)
	}
	for _, secret := range []string{marker, "Synthetic Name", "Bearer", "pw-"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("%q reached the logs", secret)
		}
	}
}
