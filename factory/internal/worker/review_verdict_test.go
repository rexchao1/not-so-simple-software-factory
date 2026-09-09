package worker

import (
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestStageReviewVerdictNeverComesFromTheImplementingStage(t *testing.T) {
	// Position 0 implements. The server refuses a verdict there, so sending
	// one would fail the whole stage completion for any single-stage Pipeline
	// whose agent printed the marker. The Worker drops it instead.
	approved := "FACTORY-VERDICT: approve"
	if got := stageReviewVerdict(0, approved); got != protocol.ReviewVerdictNone {
		t.Fatalf("the implementing stage reported verdict %q", got)
	}
	if got := stageReviewVerdict(1, approved); got != protocol.ReviewVerdictApprove {
		t.Fatalf("the reviewing stage reported verdict %q, want approve", got)
	}
	if got := stageReviewVerdict(1, "no marker here"); got != protocol.ReviewVerdictNone {
		t.Fatalf("a review with no marker reported verdict %q", got)
	}
}
