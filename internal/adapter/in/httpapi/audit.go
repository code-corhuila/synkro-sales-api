package httpapi

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// Audit results for the registration operation. The set is closed: nothing a caller sends
// can become a result value.
const (
	auditCreated  = "created"
	auditReplayed = "replayed"
	auditRejected = "rejected"
	auditConflict = "conflict"
	auditDenied   = "denied"
	auditFailed   = "failed"
)

const auditRegisterAction = "register-sale"

// sagaSteps are the only step names a Workflow idempotency key may carry.
var sagaSteps = map[string]bool{"validate-customer": true, "reserve-stock": true, "register-sale": true, "release-stock": true}

// correlationOrNew preserves the opaque trace required by the HTTP contract.
// slog JSON encodes its value; no other request header is part of the audit allowlist.
func correlationOrNew(value string) string {
	if value != "" {
		return value
	}
	return uuid.NewString()
}

// parseSagaKey extracts the saga identifier and step from a Workflow key of the form
// "<saga uuid>:<step>". Any other shape yields ok=false, and the raw key is never kept.
func parseSagaKey(key string) (sagaID, step string, ok bool) {
	id, step, found := strings.Cut(key, ":")
	if !found || !canonicalID(id) || !sagaSteps[step] {
		return "", "", false
	}
	return id, step, true
}

// auditEvent is the closed allowlist of fields written for a registration: when (the log
// record time), which service, the trace, the saga and step, who acted, who authored the sale,
// what was done and the outcome. No header, body, token, error text or personal data is a field.
type auditEvent struct {
	correlationID string
	idempotency   string
	actorID       string
	authorID      string
	result        string
}

func (d Dependencies) audit(ctx context.Context, e auditEvent) {
	attrs := []slog.Attr{
		slog.String("service", "synkro-sales-api"),
		slog.String("correlationId", correlationOrNew(e.correlationID)),
	}
	if sagaID, step, ok := parseSagaKey(e.idempotency); ok {
		attrs = append(attrs, slog.String("sagaId", sagaID), slog.String("step", step))
	}
	if actor := safeActor(e.actorID); actor != "" {
		attrs = append(attrs, slog.String("actorId", actor))
	}
	if canonicalID(e.authorID) {
		attrs = append(attrs, slog.String("authorId", e.authorID))
	}
	attrs = append(attrs, slog.String("action", auditRegisterAction), slog.String("result", e.result))
	d.Logger.LogAttrs(ctx, slog.LevelInfo, "audit", attrs...)
}

// safeActor admits only values the verifier can produce: a canonical UUID or an approved service subject.
func safeActor(subject string) string {
	if canonicalID(subject) || subject == "synkro-workflow" || subject == "synkro-worker" {
		return subject
	}
	return ""
}
