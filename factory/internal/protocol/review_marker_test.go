package protocol

import "testing"

func TestParseReviewVerdict(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result string
		want   ReviewVerdict
	}{
		{name: "absent", result: "I reviewed the change and it looks fine.", want: ReviewVerdictNone},
		{name: "empty", result: "", want: ReviewVerdictNone},
		{name: "approve", result: "Looks good.\nFACTORY-VERDICT: approve\n", want: ReviewVerdictApprove},
		{name: "request changes", result: "FACTORY-VERDICT: request-changes", want: ReviewVerdictRequestChanges},
		{name: "blocked", result: "FACTORY-VERDICT: blocked", want: ReviewVerdictBlocked},
		{name: "indented", result: "   FACTORY-VERDICT:   approve   ", want: ReviewVerdictApprove},
		{name: "mixed case value", result: "FACTORY-VERDICT: Approve", want: ReviewVerdictApprove},

		// Everything below must not approve. Each is a way a review could look
		// like an approval without being one, and INV-8 fails closed on all.
		{name: "misspelled", result: "FACTORY-VERDICT: approved", want: ReviewVerdictNone},
		{name: "lgtm", result: "FACTORY-VERDICT: lgtm", want: ReviewVerdictNone},
		{name: "nothing after the marker", result: "FACTORY-VERDICT:", want: ReviewVerdictNone},
		{name: "prose mentioning approve", result: "I would approve this.", want: ReviewVerdictNone},
		{name: "marker not at line start", result: "see FACTORY-VERDICT: approve", want: ReviewVerdictNone},

		// The last marker wins, in both directions.
		{name: "later marker overrides", result: "FACTORY-VERDICT: approve\nFACTORY-VERDICT: blocked", want: ReviewVerdictBlocked},
		{name: "later typo clears an approval", result: "FACTORY-VERDICT: approve\nFACTORY-VERDICT: maybe", want: ReviewVerdictNone},
		{name: "approval after a block", result: "FACTORY-VERDICT: blocked\nFACTORY-VERDICT: approve", want: ReviewVerdictApprove},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ParseReviewVerdict(testCase.result); got != testCase.want {
				t.Fatalf("ParseReviewVerdict(%q) = %q, want %q", testCase.result, got, testCase.want)
			}
		})
	}
}
