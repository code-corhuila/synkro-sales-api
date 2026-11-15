//go:build ignore

package main

import (
	"os"
	"strings"
	"testing"
)

// requireEveryScenario proves the evidence gate fails when any one of the scenarios is absent,
// skipped or failed, so a negative security test can never silently stop running.
func requireEveryScenario(t *testing.T, scenarios []string) {
	t.Helper()
	if len(scenarios) == 0 {
		t.Fatal("no scenarios registered")
	}
	for _, scenario := range scenarios {
		t.Run("missing "+scenario, func(t *testing.T) {
			evidenceFixture(t)
			data, err := os.ReadFile(".integration-results/tests.json")
			if err != nil {
				t.Fatal(err)
			}
			var kept []string
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if !strings.Contains(line, `"`+scenario+`"`) {
					kept = append(kept, line)
				}
			}
			if len(kept) != strings.Count(strings.TrimSpace(string(data)), "\n") {
				t.Fatalf("fixture does not contain %s", scenario)
			}
			writeEvidence(t, "tests.json", strings.Join(kept, "\n")+"\n")
			if err := verify(); err == nil {
				t.Fatalf("evidence accepted without %s", scenario)
			}
		})
		t.Run("skipped "+scenario, func(t *testing.T) {
			evidenceFixture(t)
			writeEvidence(t, "tests.json", `{"Action":"skip","Test":"`+scenario+`"}`+"\n")
			if err := verify(); err == nil {
				t.Fatalf("a skipped %s was accepted", scenario)
			}
		})
	}
}
