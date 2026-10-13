//go:build ignore

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func evidenceFixture(t *testing.T) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previous); err != nil {
			t.Error(err)
		}
	})
	if err := os.Mkdir(".integration-results", 0700); err != nil {
		t.Fatal(err)
	}
	var events strings.Builder
	for _, name := range []string{"FirstInsertion", "IdempotentReplay", "DuplicateIDDifferentKey", "ConcurrentRegistration", "ValidatorRejection", "Permissions", "UnsafeReplayRejected", "CancelledContext", "InvalidAuthentication", "UnavailableServer"} {
		event, _ := json.Marshal(map[string]string{"Action": "pass", "Test": "TestMongoIntegration/" + name})
		events.Write(event)
		events.WriteByte('\n')
	}
	writeEvidence(t, "tests.json", events.String())
	writeEvidence(t, "coverage.out", "mode: atomic\nrepo/internal/domain/model.go:1.1,2.1 10 1\nrepo/internal/application/app.go:1.1,2.1 10 1\nrepo/internal/adapter/out/persistence/mongo.go:1.1,2.1 10 1\n")
}

func writeEvidence(t *testing.T, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(".integration-results", name), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestEvidenceRequiresExecutedScenarios(t *testing.T) {
	cases := map[string]string{
		"missing scenario": "{\"Action\":\"pass\",\"Test\":\"TestMongoIntegration/FirstInsertion\"}\n",
		"parent only":      "{\"Action\":\"pass\",\"Test\":\"TestMongoIntegration\"}\n",
		"skipped suite":    "{\"Action\":\"skip\",\"Test\":\"TestMongoIntegration\"}\n",
		"failed scenario":  "{\"Action\":\"fail\",\"Test\":\"TestMongoIntegration/ConcurrentRegistration\"}\n",
		"invalid JSON":     "not JSON\n",
		"empty evidence":   "",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			evidenceFixture(t)
			writeEvidence(t, "tests.json", input)
			if err := verify(); err == nil {
				t.Fatal("invalid execution evidence accepted")
			}
		})
	}
	t.Run("all executed", func(t *testing.T) {
		evidenceFixture(t)
		if err := verify(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestEvidenceChecksLayerCoverage(t *testing.T) {
	for _, layer := range []string{"domain", "application", "infrastructure", "global"} {
		t.Run(layer, func(t *testing.T) {
			evidenceFixture(t)
			paths := map[string]string{"domain": "repo/internal/domain/model.go", "application": "repo/internal/application/app.go", "infrastructure": "repo/internal/adapter/out/persistence/mongo.go", "global": "repo/cmd/sales-api/main.go"}
			body := "mode: atomic\nrepo/internal/domain/model.go:1.1,2.1 10 1\nrepo/internal/application/app.go:1.1,2.1 10 1\nrepo/internal/adapter/out/persistence/mongo.go:1.1,2.1 10 1\n" + paths[layer] + ":3.1,4.1 100 0\n"
			writeEvidence(t, "coverage.out", body)
			if err := verify(); err == nil {
				t.Fatal("coverage below threshold accepted")
			}
		})
	}
	t.Run("missing coverage", func(t *testing.T) {
		evidenceFixture(t)
		if err := os.Remove(".integration-results/coverage.out"); err != nil {
			t.Fatal(err)
		}
		if err := verify(); err == nil {
			t.Fatal("missing coverage accepted")
		}
	})
}
