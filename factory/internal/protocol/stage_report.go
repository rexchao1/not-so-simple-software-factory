package protocol

import (
	"strings"
	"unicode/utf8"
)

// A Factory-owned stage prompt asks for a bounded report at the end of the
// agent's result, so that verification can be read rather than guessed at:
//
//	Verification:
//	- go test ./internal/controlplane - passed
//	- npm run lint - failed
//	- manual smoke of the board - not-run
//
// Everything this file parses is the agent's own claim. Factory did not run
// these commands and cannot confirm them, so every check it produces is
// labelled agent-reported and is never mixed with a code stage's exit status.
//
// The parser is conservative in one direction throughout: anything it does not
// clearly understand yields no check at all. A missing row costs the operator
// one line of detail, whereas a guessed row is Factory inventing evidence.
const (
	// StageReportVerificationHeading opens the block. It is matched on its own
	// line so that the word appearing in ordinary prose cannot start parsing.
	StageReportVerificationHeading = "Verification:"

	// MaxStageReportChecks bounds what one stage can claim. An agent that
	// emits hundreds of rows is malfunctioning, and the Outcome tab is a
	// summary rather than a log.
	MaxStageReportChecks = 20

	// maxStageReportCheckNameBytes keeps one row readable in the table it
	// renders into.
	maxStageReportCheckNameBytes = 200
)

// stageReportSeparators are the dashes an agent may use between a check and
// its result. Factory's own prompt asks for a plain hyphen; the wider dashes
// are accepted because models substitute them freely, and rejecting a row over
// punctuation would lose real evidence.
//
// Order matters only in that each candidate is validated before it is
// accepted, so a command containing " -- " is still split at the separator that
// leaves a recognised state behind.
var stageReportSeparators = []string{" — ", " – ", " -- ", " - "}

// ParseStageReportChecks reads the Verification block out of a stage result.
//
// It returns nothing at all when the heading is absent, when no line under it
// parses, or when the block is malformed. That is the fail-closed direction:
// the caller then shows the raw result, which is honest, instead of a summary
// Factory made up.
func ParseStageReportChecks(result string) []VerificationCheck {
	lines := strings.Split(result, "\n")
	start := -1
	// The last heading wins, matching ParseReviewVerdict: a resumed or
	// self-correcting agent's final report is the one that counts.
	for index, line := range lines {
		if strings.EqualFold(strings.TrimSpace(line), StageReportVerificationHeading) {
			start = index
		}
	}
	if start < 0 {
		return nil
	}

	var checks []VerificationCheck
	for _, line := range lines[start+1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		item, isItem := stageReportListItem(trimmed)
		if !isItem {
			// A non-list line ends the block. Anything after it is ordinary
			// prose, and continuing would read the next section as checks.
			break
		}
		if len(checks) >= MaxStageReportChecks {
			break
		}
		if check, ok := parseStageReportCheck(item); ok {
			checks = append(checks, check)
		}
	}
	return checks
}

// stageReportListItem strips one leading bullet. Only a bullet counts as a row,
// so a heading or a sentence following the block cannot become a check.
func stageReportListItem(line string) (string, bool) {
	for _, bullet := range []string{"- ", "* ", "• "} {
		if strings.HasPrefix(line, bullet) {
			return strings.TrimSpace(strings.TrimPrefix(line, bullet)), true
		}
	}
	return "", false
}

// parseStageReportCheck splits one row into its check and its claimed result.
// A row with no recognised separator, no name, or an unrecognised state is
// dropped rather than guessed at.
func parseStageReportCheck(item string) (VerificationCheck, bool) {
	for _, separator := range stageReportSeparators {
		// LastIndex, not Index: a command may legitimately contain a dash
		// ("go test -run TestX"), and the state is always the final field.
		cut := strings.LastIndex(item, separator)
		if cut < 0 {
			continue
		}
		name := strings.TrimSpace(item[:cut])
		state, ok := parseStageReportState(item[cut+len(separator):])
		if !ok || name == "" {
			continue
		}
		return VerificationCheck{
			Name:   boundedReportField(name, maxStageReportCheckNameBytes),
			Source: VerificationSourceAgentReported,
			State:  state,
		}, true
	}
	return VerificationCheck{}, false
}

// parseStageReportState admits only the three words the contract names. A
// fourth word means the agent did not follow the contract, and the row is
// dropped rather than mapped onto the nearest state.
func parseStageReportState(value string) (VerificationCheckState, bool) {
	switch strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(value), ".`*"))) {
	case "passed", "pass":
		return VerificationPassed, true
	case "failed", "fail":
		return VerificationFailed, true
	case "not-run", "not run", "skipped":
		return VerificationNotRun, true
	default:
		return "", false
	}
}

func boundedReportField(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	value = value[:maximum]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
