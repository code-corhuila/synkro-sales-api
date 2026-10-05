package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	TraceID string `json:"traceId"`
}

// requireAuth is a deliberately minimal default-deny gate: it rejects
// every request that lacks a well-formed Authorization header. It does
// NOT validate a real RS256 signature yet — that lands with the first
// story that needs an authenticated route to actually succeed
// (the first HU-VEN-NN story). Its only job here is to make sure
// nothing is reachable by accident.
func requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Correlation-Id")
		if traceID == "" {
			traceID = uuid.NewString()
		}
		w.Header().Set("X-Correlation-Id", traceID)

		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || !looksLikeJWT(token) {
			writeUnauthorized(w, traceID)
			return
		}
		// A real token still gets no further validation in this story —
		// any JWT-shaped Bearer token passes through. Real RS256
		// verification is explicitly out of scope here.
		next.ServeHTTP(w, r)
	})
}

// looksLikeJWT only checks the compact-serialization shape
// (header.payload.signature, all non-empty). It decodes nothing.
func looksLikeJWT(token string) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return true
}

func writeUnauthorized(w http.ResponseWriter, traceID string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	json.NewEncoder(w).Encode(errorResponse{
		Error:   "UNAUTHORIZED",
		Message: "a valid Authorization header is required",
		TraceID: traceID,
	})
}
