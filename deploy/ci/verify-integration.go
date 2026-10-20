//go:build ignore

// This evidence/coverage gate runs explicitly with go run after real tests.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	if err := verify(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func verify() error {
	f, err := os.Open(".integration-results/tests.json")
	if err != nil {
		return err
	}
	defer f.Close()
	required := map[string]bool{}
	for _, name := range []string{"FirstInsertion", "IdempotentReplay", "DuplicateIDDifferentKey", "ConcurrentRegistration", "ValidatorRejection", "Permissions", "UnsafeReplayRejected", "CancelledContext", "InvalidAuthentication", "UnavailableServer"} {
		required["TestMongoIntegration/"+name] = false
	}
	for _, name := range []string{"FirstRegistration", "Replay", "AdminListAndDetail", "SalespersonOwnership", "ForeignSaleForbidden", "UnknownAndInactiveNotFound", "PaginationUTC", "InconsistentDocumentRejected", "DatabaseFailure", "NoDeletePermission", "ReportsAdminAndSalesperson", "ReportsAccessControl", "ReportsPaginationAndMeta", "ReportsFiltersAndRouting", "ReportsDatabaseFailure"} {
		required["TestMongoIntegration/HTTP/"+name] = false
	}
	// HU-VEN-10: the three report pipelines must run against the real database.
	for _, name := range []string{"ActiveSelectionAndGrouping", "SalespersonOwnership", "UTCDayBoundaries", "MonthAndYearBoundaries", "TopProductsLinesTiesAndInactiveDetails", "PaginationOverGroups", "EmptyResults", "OverflowFailsSafely", "DatabaseFailureAndCancellation"} {
		required["TestMongoIntegration/Reports/"+name] = false
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		var event struct {
			Action string
			Test   string
			Output string
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return err
		}
		if strings.HasPrefix(event.Test, "TestMongoIntegration") && (event.Action == "skip" || event.Action == "fail") {
			return fmt.Errorf("required integration scenario %s: %s", event.Test, event.Action)
		}
		if _, ok := required[event.Test]; ok && event.Action == "pass" {
			required[event.Test] = true
		}
		if event.Output != "" {
			fmt.Print(event.Output)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for name, passed := range required {
		if !passed {
			return fmt.Errorf("no executed passing evidence for %s", name)
		}
	}
	fmt.Printf("All %d required real MongoDB integration scenarios executed and passed\n", len(required))
	return coverage()
}

func coverage() error {
	f, err := os.Open(".integration-results/coverage.out")
	if err != nil {
		return err
	}
	defer f.Close()
	type totals struct{ statements, covered int }
	counts := map[string]*totals{"domain": {}, "application": {}, "infrastructure": {}, "global": {}}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 3 {
			continue
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return err
		}
		hits, err := strconv.Atoi(fields[2])
		if err != nil {
			return err
		}
		layers := []string{"global"}
		switch {
		case strings.Contains(fields[0], "/internal/domain/"):
			layers = append(layers, "domain")
		case strings.Contains(fields[0], "/internal/application/"):
			layers = append(layers, "application")
		case strings.Contains(fields[0], "/internal/adapter/"), strings.Contains(fields[0], "/internal/config/"), strings.Contains(fields[0], "/cmd/"):
			layers = append(layers, "infrastructure")
		}
		for _, layer := range layers {
			counts[layer].statements += n
			if hits > 0 {
				counts[layer].covered += n
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	for _, layer := range []string{"domain", "application", "infrastructure", "global"} {
		minimum := map[string]float64{"domain": 90, "application": 80, "infrastructure": 60, "global": 80}[layer]
		c := counts[layer]
		if c.statements == 0 {
			return fmt.Errorf("missing %s coverage", layer)
		}
		percent := 100 * float64(c.covered) / float64(c.statements)
		fmt.Printf("%s coverage: %.2f%% (minimum %.0f%%)\n", layer, percent, minimum)
		if percent < minimum {
			return fmt.Errorf("%s coverage below threshold", layer)
		}
	}
	return nil
}
