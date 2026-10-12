//go:build ignore

package main

import (
	"os"
	"strings"
	"testing"
)

func TestEvidenceRejectsMissingHTTPIntegration(t *testing.T) {
	evidenceFixture(t)
	data, err := os.ReadFile(".integration-results/tests.json")
	if err != nil {
		t.Fatal(err)
	}
	var persistenceOnly []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "/HTTP/") {
			persistenceOnly = append(persistenceOnly, line)
		}
	}
	writeEvidence(t, "tests.json", strings.Join(persistenceOnly, "\n"))
	// Existing persistence tests alone must not satisfy HU-VEN-09 verification.
	if err := verify(); err == nil {
		t.Fatal("HTTP integration evidence was not required")
	}
}
