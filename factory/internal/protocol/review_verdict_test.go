package protocol

import "testing"

func TestAnAgentMayNotReportAMerge(t *testing.T) {
	// SupportedWorkUpdateStatus validates what an agent may send. A merge is
	// written by the control plane with actor = system and by nothing else, so
	// admitting it here would let an agent claim its own work was merged.
	if SupportedWorkUpdateStatus(WorkUpdateMerged) {
		t.Fatal("an agent is allowed to report status = merged")
	}
}

func TestSupportedReviewVerdict(t *testing.T) {
	for _, verdict := range []ReviewVerdict{
		ReviewVerdictNone, ReviewVerdictApprove,
		ReviewVerdictRequestChanges, ReviewVerdictBlocked,
	} {
		if !SupportedReviewVerdict(verdict) {
			t.Errorf("the schema admits %q but SupportedReviewVerdict rejects it", verdict)
		}
	}
	for _, verdict := range []ReviewVerdict{"lgtm", "approved", "APPROVE", "merge"} {
		if SupportedReviewVerdict(verdict) {
			t.Errorf("SupportedReviewVerdict accepted %q, which the schema CHECK would refuse", verdict)
		}
	}
}
