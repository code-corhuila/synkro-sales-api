//go:build ignore

package main

// securityScenarios286 are the real-database audit-trail and error-envelope regressions (issue 286).
// A skipped, failed or missing one fails the evidence gate.
var securityScenarios286 = []string{
	"TestMongoIntegration/Security286/AuditLogAllowlist",
	"TestMongoIntegration/Security286/CoherentSagaTrace",
	"TestMongoIntegration/Security286/ErrorEnvelopesAreGeneric",
}

func init() { extraRequired = append(extraRequired, securityScenarios286...) }
