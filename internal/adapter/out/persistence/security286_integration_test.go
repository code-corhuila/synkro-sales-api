package persistence

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func isCanonicalUUID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id.String() == value
}

func auditRecords(t *testing.T, logs string) []map[string]any {
	t.Helper()
	var audit []map[string]any
	for _, record := range decodeLogLines(t, logs) {
		if record["msg"] == "audit" {
			audit = append(audit, record)
		}
	}
	return audit
}

// runSecurity286Integration (issue 286) checks the registration audit trail of the real router
// over the real MongoDB: a closed set of fields, a coherent correlation/saga/step trace, and
// no credential, key or personal data in any log line or error body, even when the caller
// sends untrusted identity headers, keys and bodies.
func runSecurity286Integration(t *testing.T, client *mongo.Client) {
	h := newSecurityHarness(t, client)
	customer, author, product := uuid.NewString(), uuid.NewString(), uuid.NewString()
	marker := "TOKEN-MARKER-" + uuid.NewString()
	customerName, document, password := "Synthetic Customer "+uuid.NewString(), "DOC-"+uuid.NewString(), "pw-"+uuid.NewString()
	allowed := map[string]bool{"time": true, "level": true, "msg": true, "service": true, "correlationId": true, "sagaId": true, "step": true, "actorId": true, "authorId": true, "action": true, "result": true}
	body := saleBody(customer, author, product, 2, 1250)

	t.Run("AuditLogAllowlist", func(t *testing.T) {
		h.logs.Reset()
		trace, key := uuid.NewString(), sagaKey()
		workflow := h.workflowToken()
		withTrace := map[string]string{"X-Correlation-Id": trace}
		h.post(workflow, key, body, withTrace)
		h.post(workflow, key, body, withTrace)
		h.post(workflow, key, saleBody(customer, author, product, 2, 9000), withTrace)
		h.post(h.token("synkro-workflow", "SERVICE"), sagaKey(), body, withTrace)
		hostile := marker + "-hostile-key"
		unknownFields := strings.Replace(body, `"details"`, `"customerName":"`+customerName+`","identityDocument":"`+document+`","password":"`+password+`","details"`, 1)
		hostileHeaders := map[string]string{"X-Correlation-Id": uuid.NewString(), "X-Debug": marker}
		h.post(workflow, hostile, unknownFields, hostileHeaders)
		h.post(workflow, hostile, body, hostileHeaders)

		records := auditRecords(t, h.logs.String())
		wantResults := []string{"created", "replayed", "conflict", "denied", "rejected", "created"}
		if len(records) != len(wantResults) {
			t.Fatalf("audit records = %d, want %d: %s", len(records), len(wantResults), h.logs.String())
		}
		for i, record := range records {
			for field := range record {
				if !allowed[field] {
					t.Fatalf("record %d carries a field outside the allowlist: %q", i, field)
				}
			}
			if record["service"] != "synkro-sales-api" || record["action"] != "register-sale" || record["result"] != wantResults[i] {
				t.Fatalf("record %d: %v", i, record)
			}
			if correlation, _ := record["correlationId"].(string); !isCanonicalUUID(correlation) {
				t.Fatalf("record %d: correlationId is not a canonical UUID: %v", i, record["correlationId"])
			}
			if actor, _ := record["actorId"].(string); actor != "synkro-workflow" {
				t.Fatalf("record %d: actorId = %v", i, record["actorId"])
			}
		}
		for i := 0; i < 4; i++ {
			if records[i]["correlationId"] != trace {
				t.Fatalf("record %d lost the propagated trace", i)
			}
		}
		for _, i := range []int{0, 1} {
			if records[i]["authorId"] != author {
				t.Fatalf("record %d: the accepted sale author is missing", i)
			}
		}
		for _, i := range []int{4, 5} {
			if _, present := records[i]["sagaId"]; present || records[i]["correlationId"] == marker {
				t.Fatalf("record %d: hostile input became audit content: %v", i, records[i])
			}
		}
		logs := h.logs.String()
		for _, secret := range []string{marker, customerName, document, password, workflow, "Bearer", hostile, key} {
			if strings.Contains(logs, secret) {
				t.Fatalf("a secret or raw input reached the logs: %q", secret)
			}
		}
	})

	t.Run("CoherentSagaTrace", func(t *testing.T) {
		h.logs.Reset()
		trace, sagaID := uuid.NewString(), uuid.NewString()
		key := sagaID + ":register-sale"
		first := h.post(h.workflowToken(), key, body, map[string]string{"X-Correlation-Id": trace})
		replay := h.post(h.workflowToken(), key, body, map[string]string{"X-Correlation-Id": trace})
		conflict := h.post(h.workflowToken(), key, saleBody(customer, author, product, 5, 1250), map[string]string{"X-Correlation-Id": trace})
		if first.Code != 201 || replay.Code != 200 || conflict.Code != 422 {
			t.Fatalf("statuses = %d, %d, %d, want 201, 200, 422", first.Code, replay.Code, conflict.Code)
		}
		var envelope struct {
			TraceID string `json:"traceId"`
		}
		if conflict.Header().Get("X-Correlation-Id") != trace || json.Unmarshal(conflict.Body.Bytes(), &envelope) != nil || envelope.TraceID != trace {
			t.Fatal("the error envelope does not carry the propagated trace")
		}
		records := auditRecords(t, h.logs.String())
		if len(records) != 3 {
			t.Fatalf("audit records = %d, want 3", len(records))
		}
		for i, record := range records {
			if record["correlationId"] != trace || record["sagaId"] != sagaID || record["step"] != "register-sale" {
				t.Fatalf("record %d breaks the trace/saga/step coherence: %v", i, record)
			}
		}
	})

	t.Run("ErrorEnvelopesAreGeneric", func(t *testing.T) {
		unavailable := newSecurityHarness(t, integrationClient(t, "mongodb://127.0.0.1:1/sales?directConnection=true"))
		unavailable.logs.Reset()
		w := unavailable.post(unavailable.workflowToken(), sagaKey(), body, map[string]string{"X-Correlation-Id": uuid.NewString()})
		var envelope struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			TraceID string `json:"traceId"`
		}
		if w.Code != 500 || json.Unmarshal(w.Body.Bytes(), &envelope) != nil || envelope.Error != "INTERNAL_ERROR" || !isCanonicalUUID(envelope.TraceID) {
			t.Fatalf("unexpected failure envelope: %d %s", w.Code, w.Body.String())
		}
		for _, internal := range []string{"127.0.0.1", "mongo", "server selection", "topology", marker, "goroutine"} {
			if strings.Contains(strings.ToLower(w.Body.String()), strings.ToLower(internal)) || strings.Contains(strings.ToLower(unavailable.logs.String()), strings.ToLower(internal)) {
				t.Fatalf("driver or caller detail %q leaked", internal)
			}
		}
		// A conflict reveals nothing that was stored or sent.
		key := sagaKey()
		h.post(h.workflowToken(), key, body, nil)
		conflict := h.post(h.workflowToken(), key, saleBody(customer, author, product, 9, 1250), nil)
		if conflict.Code != 422 {
			t.Fatalf("conflict answered %d", conflict.Code)
		}
		for _, stored := range []string{customer, author, product, h.stored(key).ID()} {
			if strings.Contains(conflict.Body.String(), stored) {
				t.Fatalf("the conflict response revealed stored data %q", stored)
			}
		}
	})
}
