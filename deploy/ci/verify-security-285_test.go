//go:build ignore

package main

import "testing"

func TestEvidenceRequiresEverySecurity285Scenario(t *testing.T) {
	requireEveryScenario(t, securityScenarios285)
}
