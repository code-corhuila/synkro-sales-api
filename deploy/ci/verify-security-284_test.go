//go:build ignore

package main

import "testing"

func TestEvidenceRequiresEverySecurity284Scenario(t *testing.T) {
	requireEveryScenario(t, securityScenarios284)
}
