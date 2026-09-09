package controlplane

import (
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestStageDefaults(t *testing.T) {
	store := newTestStore(t)
	t.Run("a fresh factory has no defaults", func(t *testing.T) {
		defaults, err := store.StageDefaults(t.Context())
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if defaults.Model != "" || defaults.Effort != "" {
			t.Fatalf("got %+v, want empty; an unconfigured factory must change nothing", defaults)
		}
	})
	t.Run("saving round-trips", func(t *testing.T) {
		if _, err := store.SaveStageDefaults(t.Context(),
			protocol.StageDefaults{Model: "sonnet", Effort: "medium"}); err != nil {
			t.Fatalf("save: %v", err)
		}
		defaults, err := store.StageDefaults(t.Context())
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if defaults.Model != "sonnet" || defaults.Effort != "medium" {
			t.Fatalf("got %+v, want sonnet/medium", defaults)
		}
	})
	t.Run("clearing is allowed", func(t *testing.T) {
		if _, err := store.SaveStageDefaults(t.Context(), protocol.StageDefaults{}); err != nil {
			t.Fatalf("clear: %v", err)
		}
		defaults, _ := store.StageDefaults(t.Context())
		if !(defaults.Model == "" && defaults.Effort == "") {
			t.Fatalf("got %+v, want cleared", defaults)
		}
	})
	t.Run("unknown values are refused", func(t *testing.T) {
		_, err := store.SaveStageDefaults(t.Context(), protocol.StageDefaults{Effort: "extreme"})
		requireServiceError(t, err, "invalid_stage_default_effort")
		_, err = store.SaveStageDefaults(t.Context(), protocol.StageDefaults{Model: "gpt-5"})
		requireServiceError(t, err, "invalid_stage_default_model")
	})
}
