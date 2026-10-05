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
