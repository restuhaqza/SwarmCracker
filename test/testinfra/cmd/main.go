// Command testinfra runs infrastructure checks standalone (no test harness).
//
// Usage:
//
//	go run ./test/testinfra/cmd/...              # text output
//	go run ./test/testinfra/cmd/... --json       # JSON output
//	go run ./test/testinfra/cmd/... --required    # only required checks
//
// Exit code 0 when all required checks pass, 1 otherwise.
package main

import (
	"context"
	"flag"
	"os"

	"github.com/restuhaqza/swarmcracker/test/testinfra"
)

func main() {
	jsonOut := flag.Bool("json", false, "output JSON instead of text")
	requiredOnly := flag.Bool("required", false, "only run required checks")
	flag.Parse()

	runner := testinfra.NewRunner()
	report := runner.Run(context.Background())

	if *jsonOut {
		if *requiredOnly {
			filterRequired(&report)
		}
		report.PrintJSON()
	} else {
		report.PrintText()
	}

	if !report.Ready {
		os.Exit(1)
	}
}

func filterRequired(r *testinfra.InfraReport) {
	filtered := make([]testinfra.CheckResult, 0, len(r.Results))
	passed, failed, skipped := 0, 0, 0
	for _, c := range r.Results {
		if c.Severity == testinfra.Required {
			filtered = append(filtered, c)
			switch c.Status {
			case "pass":
				passed++
			case "fail":
				failed++
			case "skip":
				skipped++
			}
		}
	}
	r.Results = filtered
	r.Passed = passed
	r.Failed = failed
	r.Skipped = skipped
}
