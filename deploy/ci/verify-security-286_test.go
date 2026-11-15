//go:build ignore

package main

import "testing"

func TestEvidenceRequiresEverySecurity286Scenario(t *testing.T) {
	requireEveryScenario(t, securityScenarios286)
}
