package persistence

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/code-corhuila/synkro-sales-api/internal/adapter/in/httpapi"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// retryVariant is the same request with exactly one business input changed.
type retryVariant struct {
	name string
	body func(customer, author, product string) string
}

// runSecurity285Integration (issue 285) proves, over the real router, use case and MongoDB, that
// the author recorded on a sale is the one the trusted Workflow supplied, that Sales computes the
// amounts, and that a reused Idempotency-Key answers with the original sale only for the same
// business input. The repository-level scenarios run first, then the same rules through HTTP.
func runSecurity285Integration(t *testing.T, client *mongo.Client) {
	runIdempotencyIntegration(t, client)

	h := newSecurityHarness(t, client)
	customer, author, product := uuid.NewString(), uuid.NewString(), uuid.NewString()

	t.Run("AuthorPersistedAsSuppliedByTrustedWorkflow", func(t *testing.T) {
		key := sagaKey()
		w := h.post(h.workflowToken(), key, saleBody(customer, author, product, 2, 1250), nil)
		if w.Code != 201 || h.countKey(key) != 1 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		raw, err := h.collection.FindOne(h.ctx, bson.D{{Key: "idempotencyKey", Value: key}}).Raw()
		if err != nil || raw.Lookup("createdBy").StringValue() != author {
			t.Fatalf("the persisted author is not the one the Workflow supplied: %v", err)
		}
	})

	t.Run("NoServiceCredentialNoWrite", func(t *testing.T) {
		key := sagaKey()
		before := h.total()
		for _, token := range []string{"", h.token(author, "ADMIN", "sales:register", "sales:read"), h.token(author, "SALESPERSON", "sales:register")} {
			w := h.post(token, key, saleBody(customer, author, product, 1, 100), nil)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("a caller without the service credential got %d", w.Code)
			}
		}
		if h.total() != before || h.countKey(key) != 0 {
			t.Fatal("a request without the service credential wrote a sale")
		}
	})

	t.Run("AmountsComputedBySales", func(t *testing.T) {
		key := sagaKey()
		forged := strings.Replace(saleBody(customer, author, product, 2, 1250), `"details"`, `"totalCents":1,"details"`, 1)
		before := h.total()
		if w := h.post(h.workflowToken(), key, forged, nil); w.Code != 400 {
			t.Fatalf("a caller-supplied total was accepted: %d %s", w.Code, w.Body.String())
		}
		forgedLine := strings.Replace(saleBody(customer, author, product, 2, 1250), `"quantity":2`, `"quantity":2,"subtotalCents":1`, 1)
		if w := h.post(h.workflowToken(), key, forgedLine, nil); w.Code != 400 {
			t.Fatalf("a caller-supplied subtotal was accepted: %d %s", w.Code, w.Body.String())
		}
		if h.total() != before {
			t.Fatal("a rejected amount override wrote a sale")
		}
		if w := h.post(h.workflowToken(), key, saleBody(customer, author, product, 3, 1250), nil); w.Code != 201 {
			t.Fatalf("registration failed: %d %s", w.Code, w.Body.String())
		}
		raw, err := h.collection.FindOne(h.ctx, bson.D{{Key: "idempotencyKey", Value: key}}).Raw()
		if err != nil || raw.Lookup("totalCents").Int64() != 3750 {
			t.Fatalf("Sales did not compute the total: %v", err)
		}
	})

	t.Run("RetrySemanticsOverHTTP", func(t *testing.T) {
		key := sagaKey()
		original := saleBody(customer, author, product, 2, 1250)
		first := h.post(h.workflowToken(), key, original, nil)
		var created httpapi.SaleResponse
		if first.Code != 201 || json.Unmarshal(first.Body.Bytes(), &created) != nil {
			t.Fatalf("setup registration: %d %s", first.Code, first.Body.String())
		}
		exact := h.post(h.workflowToken(), key, original, nil)
		if exact.Code != 200 || exact.Body.String() != first.Body.String() {
			t.Fatalf("an exact retry did not return the original: %d %s", exact.Code, exact.Body.String())
		}
		variants := []retryVariant{
			{"price", func(c, a, p string) string { return saleBody(c, a, p, 2, 9000) }},
			{"quantity", func(c, a, p string) string { return saleBody(c, a, p, 3, 1250) }},
			{"customer", func(_, a, p string) string { return saleBody(uuid.NewString(), a, p, 2, 1250) }},
			{"actor", func(c, _, p string) string { return saleBody(c, uuid.NewString(), p, 2, 1250) }},
			{"product", func(c, a, _ string) string { return saleBody(c, a, uuid.NewString(), 2, 1250) }},
		}
		for _, v := range variants {
			w := h.post(h.workflowToken(), key, v.body(customer, author, product), nil)
			if w.Code != 422 || !strings.Contains(w.Body.String(), `"error":"BUSINESS_RULE_VIOLATION"`) {
				t.Fatalf("%s: a conflicting retry answered %d %s", v.name, w.Code, w.Body.String())
			}
			for _, leaked := range []string{created.SaleID, customer, author, product} {
				if strings.Contains(w.Body.String(), leaked) {
					t.Fatalf("%s: the conflict response revealed stored data %q", v.name, leaked)
				}
			}
		}
		if h.countKey(key) != 1 {
			t.Fatal("retries changed the number of stored sales")
		}
		stored := h.stored(key)
		if stored.ID() != created.SaleID || stored.TotalCents() != 2500 || stored.CustomerID() != customer || stored.CreatedBy() != author {
			t.Fatal("a conflicting retry altered the stored sale")
		}
	})

	t.Run("ConcurrentConflictingHTTPRequests", func(t *testing.T) {
		const n = 20
		key := sagaKey()
		statuses := make(chan int, n)
		start := make(chan struct{})
		var workers sync.WaitGroup
		for i := 0; i < n; i++ {
			body := saleBody(customer, author, product, 2, 1250)
			if i%2 == 1 {
				body = saleBody(customer, author, product, 2, 7777)
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				statuses <- h.post(h.workflowToken(), key, body, nil).Code
			}()
		}
		close(start)
		workers.Wait()
		close(statuses)
		created := 0
		for status := range statuses {
			switch status {
			case 201:
				created++
			case 200, 422:
			default:
				t.Fatalf("unexpected status %d in the race", status)
			}
		}
		if created != 1 || h.countKey(key) != 1 {
			t.Fatalf("created=%d stored=%d, want exactly one sale", created, h.countKey(key))
		}
	})
}
