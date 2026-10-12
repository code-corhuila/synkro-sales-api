//go:build ignore

package main

import "testing"

func TestEvidenceRejectsMissingHTTPIntegration(t *testing.T) {
 evidenceFixture(t)
 // Existing persistence tests alone must not satisfy HU-VEN-09 verification.
 if err := verify(); err == nil { t.Fatal("HTTP integration evidence was not required") }
}
