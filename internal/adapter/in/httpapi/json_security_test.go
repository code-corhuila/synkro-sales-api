package httpapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistrationRejectsAmbiguousJSONFields(t *testing.T) {
	f := newHTTPFixture(t)
	for _, body := range []string{
		strings.ReplaceAll(requestBody, `"quantity"`, `"QUANTITY"`),
		strings.ReplaceAll(requestBody, `"customerId"`, `"CustomerId"`),
		strings.ReplaceAll(requestBody, `"quantity":2`, `"quantity":1,"quantity":2`),
		strings.ReplaceAll(requestBody, `"createdBy":"`+ownerID+`"`, `"createdBy":"`+otherID+`","createdBy":"`+ownerID+`"`),
	} {
		w := f.request(t, "POST", "/api/v1/sales", body, "synkro-workflow", "SERVICE", "sales:register")
		assertHTTPError(t, w, 400, "VALIDATION_ERROR")
	}
}

func TestJWTRejectsDuplicateSignedClaims(t *testing.T) {
	f := newHTTPFixture(t)
	claims, _ := json.Marshal(claimsAt(f.now, ownerID, "ADMIN", "sales:read"))
	payload := strings.ReplaceAll(string(claims), `"roles":["ADMIN"]`, `"roles":["INVENTORY"],"roles":["ADMIN"]`)
	// A valid RSA signature must not make an ambiguous claim set acceptable.
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	unsigned := h + "." + base64.RawURLEncoding.EncodeToString([]byte(payload))
	hash := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	token := unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
	r := httptest.NewRequest("GET", "/api/v1/sales", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, r)
	assertHTTPError(t, w, 401, "UNAUTHORIZED")
}
