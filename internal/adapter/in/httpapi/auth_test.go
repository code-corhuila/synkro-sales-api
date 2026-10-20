package httpapi

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fixedClock struct{ instant time.Time }

func (c fixedClock) Now() time.Time { return c.instant }

func testKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: raw}))
}
func claimsAt(now time.Time, sub, role string, permissions ...string) map[string]any {
	return map[string]any{"sub": sub, "roles": []string{role}, "permissions": permissions, "iat": now.Add(-time.Minute).Unix(), "exp": now.Add(time.Minute).Unix(), "jti": "test-token-id"}
}
func signToken(t *testing.T, key *rsa.PrivateKey, alg string, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": alg, "typ": "JWT"})
	p, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	digest := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestRS256Authentication(t *testing.T) {
	key, public := testKey(t)
	other, _ := testKey(t)
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	verifier, err := NewJWTVerifier(public, fixedClock{now})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		mutate func(map[string]any)
		alg    string
		key    *rsa.PrivateKey
		want   int
	}{
		{name: "valid", want: 204},
		{name: "none", alg: "none", want: 401}, {name: "HS256", alg: "HS256", want: 401}, {name: "wrong key", key: other, want: 401},
		{name: "expired", mutate: func(c map[string]any) { c["exp"] = now.Add(-31 * time.Second).Unix() }, want: 401},
		{name: "skew inside", mutate: func(c map[string]any) { c["exp"] = now.Add(-29 * time.Second).Unix() }, want: 204},
		{name: "skew boundary", mutate: func(c map[string]any) { c["exp"] = now.Add(-30 * time.Second).Unix() }, want: 401},
		{name: "future issued", mutate: func(c map[string]any) { c["iat"] = now.Add(31 * time.Second).Unix() }, want: 401},
		{name: "invalid chronology", mutate: func(c map[string]any) { c["iat"] = c["exp"] }, want: 401},
		{name: "string exp", mutate: func(c map[string]any) { c["exp"] = "123" }, want: 401},
		{name: "invalid subject", mutate: func(c map[string]any) { c["sub"] = "someone" }, want: 401},
		{name: "invalid role", mutate: func(c map[string]any) { c["roles"] = []string{"OWNER"} }, want: 401},
		{name: "mixed roles", mutate: func(c map[string]any) { c["roles"] = []string{"ADMIN", "SERVICE"} }, want: 401},
		{name: "permissions type", mutate: func(c map[string]any) { c["permissions"] = "sales:read" }, want: 401},
	}
	for _, field := range []string{"sub", "roles", "permissions", "iat", "exp", "jti"} {
		f := field
		cases = append(cases, struct {
			name   string
			mutate func(map[string]any)
			alg    string
			key    *rsa.PrivateKey
			want   int
		}{name: "missing " + f, mutate: func(c map[string]any) { delete(c, f) }, want: 401})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := claimsAt(now, "550e8400-e29b-41d4-a716-446655440000", "ADMIN", "sales:read")
			if tc.mutate != nil {
				tc.mutate(c)
			}
			alg := tc.alg
			if alg == "" {
				alg = "RS256"
			}
			signer := tc.key
			if signer == nil {
				signer = key
			}
			req := httptest.NewRequest("GET", "/", nil)
			req.Header.Set("Authorization", "Bearer "+signToken(t, signer, alg, c))
			req.Header.Set("X-User-Id", "forged")
			req.Header.Set("X-User-Roles", "INVENTORY")
			w := httptest.NewRecorder()
			requireAuth(verifier, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				p := principalFrom(r.Context())
				if p.Subject != c["sub"] || p.Role != "ADMIN" {
					t.Fatal("signed identity was overridden")
				}
				w.WriteHeader(204)
			})).ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", w.Code, tc.want, w.Body.String())
			}
		})
	}
	for _, header := range []string{"", "Bearer not-a-token", "Bearer a.b.c", "Basic a.b.c", "Bearer ", "Bearer a.b.c extra"} {
		t.Run(header, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.Header.Set("Authorization", header)
			w := httptest.NewRecorder()
			requireAuth(verifier, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid credential passed") })).ServeHTTP(w, r)
			if w.Code != 401 {
				t.Fatal(w.Code)
			}
		})
	}
}

func TestJWTKeyFailClosed(t *testing.T) {
	_, public := testKey(t)
	if _, err := NewJWTVerifier(strings.ReplaceAll(public, "\n", `\n`), fixedClock{}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"", "not PEM", public + public, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("invalid")}))} {
		if _, err := NewJWTVerifier(value, fixedClock{}); err == nil {
			t.Fatal("invalid key accepted")
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer a.b.c")
	w := httptest.NewRecorder()
	requireAuth(nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("missing verifier passed") })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
}

func TestSignedAuthorizationMatrix(t *testing.T) {
	for _, tc := range []struct {
		p              Principal
		register, read bool
	}{
		{Principal{Subject: "synkro-workflow", Role: "SERVICE", Permissions: []string{"sales:register"}}, true, false},
		{Principal{Subject: "synkro-worker", Role: "SERVICE", Permissions: []string{"sales:register", "sales:read"}}, false, false},
		{Principal{Subject: "synkro-workflow", Role: "SERVICE", Permissions: []string{}}, false, false},
		{Principal{Role: "ADMIN", Permissions: []string{"sales:read", "sales:register"}}, false, true},
		{Principal{Role: "SALESPERSON", Permissions: []string{"sales:read", "sales:register"}}, false, true},
		{Principal{Role: "INVENTORY", Permissions: []string{"sales:read", "sales:register"}}, false, false},
		{Principal{Role: "ADMIN", Permissions: []string{}}, false, false},
		{Principal{Role: "SALESPERSON", Permissions: []string{}}, false, false},
	} {
		if tc.p.CanRegister() != tc.register || tc.p.CanRead() != tc.read {
			t.Fatalf("unexpected authorization for %+v", tc.p)
		}
	}
}
