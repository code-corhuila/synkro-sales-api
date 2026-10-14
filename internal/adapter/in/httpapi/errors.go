package httpapi

import (
	"encoding/json"
	"github.com/google/uuid"
	"net/http"
)

type ErrorDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}
type errorResponse struct {
	Error   string        `json:"error"`
	Message string        `json:"message"`
	TraceID string        `json:"traceId"`
	Details []ErrorDetail `json:"details,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
func writeError(w http.ResponseWriter, status int, code, message string, details []ErrorDetail) {
	trace := w.Header().Get("X-Correlation-Id")
	if trace == "" {
		trace = uuid.NewString()
		w.Header().Set("X-Correlation-Id", trace)
	}
	writeJSON(w, status, errorResponse{Error: code, Message: message, TraceID: trace, Details: details})
}
