//go:build ignore

package main

// securityScenarios285 are the real-database author, amount and idempotency-conflict regressions
// (issue 285). A skipped, failed or missing one fails the evidence gate.
var securityScenarios285 = []string{
	"TestMongoIntegration/Security285/ConflictingInputIsRejectedAndOriginalKept",
	"TestMongoIntegration/Security285/ExactAndReorderedRetriesReturnTheOriginalOnce",
	"TestMongoIntegration/Security285/ConcurrentConflictingInsertsPersistExactlyOne",
	"TestMongoIntegration/Security285/AuthorPersistedAsSuppliedByTrustedWorkflow",
	"TestMongoIntegration/Security285/NoServiceCredentialNoWrite",
	"TestMongoIntegration/Security285/AmountsComputedBySales",
	"TestMongoIntegration/Security285/RetrySemanticsOverHTTP",
	"TestMongoIntegration/Security285/ConcurrentConflictingHTTPRequests",
}

func init() { extraRequired = append(extraRequired, securityScenarios285...) }
