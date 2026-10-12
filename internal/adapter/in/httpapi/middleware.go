package httpapi

import (
	"context"
	"net/http"
	"strings"
)

func requireAuth(verifier *JWTVerifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || len(r.Header.Values("Authorization")) != 1 {
			writeError(w, 401, "UNAUTHORIZED", "Missing or invalid authentication token", nil)
			return
		}
		p, err := verifier.Verify(token)
		if err != nil {
			writeError(w, 401, "UNAUTHORIZED", "Missing or invalid authentication token", nil)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
