package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth_ReturnsOkWithNoToken(t *testing.T) {
	srv := httptest.NewServer(NewRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Status  string `json:"status"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("expected status ok, got %q", body.Status)
	}
	if body.Service != "synkro-sales-api" {
		t.Errorf("expected service synkro-sales-api, got %q", body.Service)
	}
}

func TestProtectedRoute_Returns401WithNoToken(t *testing.T) {
	srv := httptest.NewServer(NewRouter())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sales/some-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", resp.StatusCode)
	}

	var body struct {
		Error   string `json:"error"`
		TraceID string `json:"traceId"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if body.Error != "UNAUTHORIZED" {
		t.Errorf("expected error UNAUTHORIZED, got %q", body.Error)
	}
	if body.TraceID == "" {
		t.Error("expected a non-empty traceId")
	}
}

func TestProtectedRoute_Returns401WithMalformedToken(t *testing.T) {
	srv := httptest.NewServer(NewRouter())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/sales/some-id", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-token")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 even with a malformed token, got %d", resp.StatusCode)
	}
}
