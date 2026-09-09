package protocol

import (
	"strings"
	"testing"
)

func TestParseStageReportChecksReadsTheContractedBlock(t *testing.T) {
	checks := ParseStageReportChecks(`Changes:
- Guarded the claim lease with a compare-and-set.

Verification:
- go test ./internal/controlplane — passed
- npm run lint — failed
- manual smoke of the board — not-run

Risk:
- None.`)
	if len(checks) != 3 {
		t.Fatalf("parsed %d checks, want 3: %+v", len(checks), checks)
	}
	for _, check := range checks {
		if check.Source != VerificationSourceAgentReported {
			t.Fatalf("check %q is labelled %q, want agent-reported", check.Name, check.Source)
		}
	}
	if checks[0].Name != "go test ./internal/controlplane" || checks[0].State != VerificationPassed {
		t.Fatalf("first check = %+v", checks[0])
	}
	if checks[1].State != VerificationFailed || checks[2].State != VerificationNotRun {
		t.Fatalf("states = %q, %q", checks[1].State, checks[2].State)
	}
	// The Risk section follows the block and must not become a check.
	for _, check := range checks {
		if strings.Contains(check.Name, "None") {
			t.Fatalf("prose after the block was parsed as a check: %+v", check)
		}
	}
}

// The whole point of the parser is that it never invents evidence. Every case
// here must yield nothing rather than a plausible guess.
func TestParseStageReportChecksRefusesToGuess(t *testing.T) {
	for name, result := range map[string]string{
		"no block at all":      "I ran the tests and everything passed.",
		"heading inside prose": "I updated the Verification: section of the README.",
		"no separator":         "Verification:\n- go test ./... passed",
		"unrecognised state":   "Verification:\n- go test ./... — mostly fine",
		"a claimed test count": "Verification:\n- 1,248 tests — all green",
		"empty name":           "Verification:\n-  — passed",
		"not a list":           "Verification:\nEverything passed.",
		"empty result":         "",
	} {
		t.Run(name, func(t *testing.T) {
			if checks := ParseStageReportChecks(result); len(checks) != 0 {
				t.Fatalf("parsed %+v from input that does not follow the contract", checks)
			}
		})
	}
}

// A command may contain a dash of its own, so the state has to be taken from
// the last separator rather than the first.
func TestParseStageReportChecksKeepsDashesInsideCommands(t *testing.T) {
	checks := ParseStageReportChecks("Verification:\n- go test -run TestClaim ./internal/... — passed")
	if len(checks) != 1 {
		t.Fatalf("parsed %d checks, want 1", len(checks))
	}
	if checks[0].Name != "go test -run TestClaim ./internal/..." {
		t.Fatalf("check name = %q, want the whole command", checks[0].Name)
	}
}

// One malformed row must not discard the rows around it: the operator keeps
// the evidence that was well formed.
func TestParseStageReportChecksSkipsOnlyTheMalformedRow(t *testing.T) {
	checks := ParseStageReportChecks(`Verification:
- go test ./... — passed
- something vague
- go vet ./... — passed`)
	if len(checks) != 2 {
		t.Fatalf("parsed %d checks, want the 2 well-formed ones: %+v", len(checks), checks)
	}
}

func TestParseStageReportChecksAcceptsCommonSubstitutions(t *testing.T) {
	for name, line := range map[string]string{
		"en dash":      "- go test ./... – passed",
		"double dash":  "- go test ./... -- passed",
		"plain dash":   "- go test ./... - passed",
		"asterisk":     "* go test ./... — passed",
		"bullet":       "• go test ./... — passed",
		"pass":         "- go test ./... — pass",
		"skipped":      "- go test ./... — skipped",
		"trailing dot": "- go test ./... — passed.",
	} {
		t.Run(name, func(t *testing.T) {
			if checks := ParseStageReportChecks("Verification:\n" + line); len(checks) != 1 {
				t.Fatalf("parsed %d checks from %q, want 1", len(checks), line)
			}
		})
	}
}

func TestParseStageReportChecksIsBounded(t *testing.T) {
	var builder strings.Builder
	builder.WriteString("Verification:\n")
	for index := 0; index < MaxStageReportChecks*5; index++ {
		builder.WriteString("- check " + strings.Repeat("x", 500) + " — passed\n")
	}
	checks := ParseStageReportChecks(builder.String())
	if len(checks) != MaxStageReportChecks {
		t.Fatalf("parsed %d checks, want the bound of %d", len(checks), MaxStageReportChecks)
	}
	for _, check := range checks {
		if len(check.Name) > maxStageReportCheckNameBytes {
			t.Fatalf("check name is %d bytes, over its bound", len(check.Name))
		}
	}
}

// A resumed or self-correcting agent may emit the block twice. The final
// report is the one that counts, matching ParseReviewVerdict.
func TestParseStageReportChecksUsesTheLastBlock(t *testing.T) {
	checks := ParseStageReportChecks(`Verification:
- first attempt — failed

I fixed it and reran.

Verification:
- go test ./... — passed`)
	if len(checks) != 1 || checks[0].State != VerificationPassed {
		t.Fatalf("checks = %+v, want only the final block", checks)
	}
}
