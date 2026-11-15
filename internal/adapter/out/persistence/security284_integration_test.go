package persistence

import (
	"crypto/rand"
	"crypto/rsa"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// deniedCase is one request that must be rejected before anything is written.
type deniedCase struct {
	name  string
	token string
	extra map[string]string
}

// runSecurity284Integration (issue 284) drives POST /api/v1/sales through the real router,
// use case and MongoDB. Only the Workflow service credential with sales:register may register
// a sale; every other caller is rejected before anything is written, which is proven by the
// number of documents in the real collection, including when a denied request replays the
// key of a stored sale.
func runSecurity284Integration(t *testing.T, client *mongo.Client) {
	h := newSecurityHarness(t, client)
	customer, author, product := uuid.NewString(), uuid.NewString(), uuid.NewString()
	body := saleBody(customer, author, product, 2, 1250)

	assertDenied := func(t *testing.T, c deniedCase, status int, errorCode string) {
		t.Helper()
		key := sagaKey()
		before := h.total()
		w := h.post(c.token, key, body, c.extra)
		if w.Code != status || !strings.Contains(w.Body.String(), `"error":"`+errorCode+`"`) {
			t.Fatalf("%s: status=%d body=%s, want %d %s", c.name, w.Code, w.Body.String(), status, errorCode)
		}
		if after := h.total(); after != before || h.countKey(key) != 0 {
			t.Fatalf("%s: a denied request changed the database (%d -> %d documents)", c.name, before, after)
		}
	}

	t.Run("UnauthenticatedRejected", func(t *testing.T) {
		wrongKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		expired := h.claims("synkro-workflow", "SERVICE", "sales:register")
		expired["iat"], expired["exp"] = h.clock.Now().Add(-20*time.Minute).Unix(), h.clock.Now().Add(-10*time.Minute).Unix()
		valid := h.workflowToken()
		spoofed := map[string]string{"X-User-Id": author, "X-User-Roles": "ADMIN", "X-Service-Name": "synkro-workflow", "X-Permissions": "sales:register"}
		cases := []deniedCase{
			{name: "missing"},
			{name: "empty bearer", extra: map[string]string{"Authorization": "Bearer "}},
			{name: "not a jwt", token: "not-a-jwt"},
			{name: "two segments", token: "abc.def"},
			{name: "basic scheme", extra: map[string]string{"Authorization": "Basic " + valid}},
			{name: "lowercase scheme", extra: map[string]string{"Authorization": "bearer " + valid}},
			{name: "expired", token: h.sign(h.key, "RS256", expired)},
			{name: "wrong signature", token: h.sign(wrongKey, "RS256", h.claims("synkro-workflow", "SERVICE", "sales:register"))},
			{name: "unsigned alg none", token: h.sign(h.key, "none", h.claims("synkro-workflow", "SERVICE", "sales:register"))},
			{name: "hmac algorithm", token: h.sign(h.key, "HS256", h.claims("synkro-workflow", "SERVICE", "sales:register"))},
			{name: "tampered payload", token: tamperedPayload(valid)},
			{name: "spoofed identity headers only", extra: spoofed},
		}
		for _, c := range cases {
			assertDenied(t, c, 401, "UNAUTHORIZED")
		}
	})

	t.Run("ForbiddenWithoutPermission", func(t *testing.T) {
		cases := []deniedCase{
			{name: "no permissions", token: h.token("synkro-workflow", "SERVICE")},
			{name: "read permission only", token: h.token("synkro-workflow", "SERVICE", "sales:read")},
			{name: "unrelated permissions", token: h.token("synkro-workflow", "SERVICE", "products:write", "reports:read")},
		}
		for _, c := range cases {
			assertDenied(t, c, 403, "FORBIDDEN")
		}
	})

	t.Run("ForbiddenOtherService", func(t *testing.T) {
		cases := []deniedCase{
			{name: "worker service with the permission", token: h.token("synkro-worker", "SERVICE", "sales:register")},
			{name: "human ADMIN with the permission", token: h.token(author, "ADMIN", "sales:register")},
			{name: "human SALESPERSON with the permission", token: h.token(author, "SALESPERSON", "sales:register")},
			{name: "human INVENTORY with the permission", token: h.token(author, "INVENTORY", "sales:register")},
		}
		for _, c := range cases {
			assertDenied(t, c, 403, "FORBIDDEN")
		}
	})

	t.Run("SpoofedHeadersGiveNoBypass", func(t *testing.T) {
		spoof := map[string]string{"X-User-Id": author, "X-User-Roles": "SERVICE", "X-Service-Name": "synkro-workflow", "X-Permissions": "sales:register", "X-Forwarded-User": "synkro-workflow"}
		cases := []deniedCase{
			{name: "spoofed headers beside a read-only token", token: h.token(author, "ADMIN", "sales:read"), extra: spoof},
			{name: "spoofed headers beside the worker token", token: h.token("synkro-worker", "SERVICE", "sales:register"), extra: spoof},
		}
		for _, c := range cases {
			assertDenied(t, c, 403, "FORBIDDEN")
		}
	})

	t.Run("AuthorizedWorkflowCreatesExactlyOne", func(t *testing.T) {
		key := sagaKey()
		before := h.total()
		w := h.post(h.workflowToken(), key, body, nil)
		if w.Code != 201 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if h.total() != before+1 || h.countKey(key) != 1 {
			t.Fatal("an authorized registration did not persist exactly one sale")
		}
		stored := h.stored(key)
		if stored.CustomerID() != customer || stored.CreatedBy() != author || stored.TotalCents() != 2500 {
			t.Fatal("persisted sale does not match the authorized request")
		}
	})

	t.Run("DeniedReplayDoesNotTouchStoredSale", func(t *testing.T) {
		key := sagaKey()
		if w := h.post(h.workflowToken(), key, body, nil); w.Code != 201 {
			t.Fatalf("setup registration: %d %s", w.Code, w.Body.String())
		}
		saleID := h.stored(key).ID()
		before := h.total()
		cases := []deniedCase{
			{name: "no credential"},
			{name: "no permission", token: h.token("synkro-workflow", "SERVICE")},
			{name: "other service", token: h.token("synkro-worker", "SERVICE", "sales:register")},
			{name: "human with permission", token: h.token(author, "ADMIN", "sales:register")},
		}
		for _, c := range cases {
			w := h.post(c.token, key, body, c.extra)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("%s: denied replay answered %d", c.name, w.Code)
			}
			if strings.Contains(w.Body.String(), saleID) || strings.Contains(w.Body.String(), customer) {
				t.Fatalf("%s: a denied replay revealed the stored sale", c.name)
			}
			if h.total() != before || h.countKey(key) != 1 {
				t.Fatalf("%s: a denied replay changed the database", c.name)
			}
		}
		// The legitimate caller still gets the original, once.
		if w := h.post(h.workflowToken(), key, body, nil); w.Code != 200 || !strings.Contains(w.Body.String(), saleID) || h.countKey(key) != 1 {
			t.Fatalf("authorized replay: %d %s", w.Code, w.Body.String())
		}
	})
}

// tamperedPayload keeps the header and signature of a valid token but swaps the claims for
// ones the signature does not cover.
func tamperedPayload(token string) string {
	parts := strings.Split(token, ".")
	forged := "eyJzdWIiOiJzeW5rcm8td29ya2Zsb3ciLCJyb2xlcyI6WyJTRVJWSUNFIl0sInBlcm1pc3Npb25zIjpbInNhbGVzOnJlZ2lzdGVyIl0sImlhdCI6MSwiZXhwIjo5OTk5OTk5OTk5LCJqdGkiOiJ4In0"
	return parts[0] + "." + forged + "." + parts[2]
}
