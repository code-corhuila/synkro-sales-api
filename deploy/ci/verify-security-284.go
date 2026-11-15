//go:build ignore

package main

// securityScenarios284 are the real-database registration-authorization regressions (issue 284).
// A skipped, failed or missing one fails the evidence gate.
var securityScenarios284 = []string{
	"TestMongoIntegration/Security284/UnauthenticatedRejected",
	"TestMongoIntegration/Security284/ForbiddenWithoutPermission",
	"TestMongoIntegration/Security284/ForbiddenOtherService",
	"TestMongoIntegration/Security284/SpoofedHeadersGiveNoBypass",
	"TestMongoIntegration/Security284/AuthorizedWorkflowCreatesExactlyOne",
	"TestMongoIntegration/Security284/DeniedReplayDoesNotTouchStoredSale",
}

func init() { extraRequired = append(extraRequired, securityScenarios284...) }
