package controlplane

import (
	"net/http"
	"strconv"

	"github.com/owainlewis/factory/internal/protocol"
)

// The planning API is what the light-factory skill writes through. It is on
// the loopback operator handler with every other write route, so it is reached
// from the MacBook the same way the cockpit is, over `tailscale serve`, and
// never binds anything wider.
//
// Every route addresses its subject by (project, number) in the path, so there
// is no request body field that could disagree with the URL. Each write
// returns the stored resource rather than an empty 200, because the caller is
// an agent that then has to reason about what it just saved, and a second GET
// to find out is a race it does not need.

// listPlanningProjects serves every project with its checkpoint count.
func (a *API) listPlanningProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := a.store.PlanningProjects(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": projects})
}

func (a *API) getPlanningProject(w http.ResponseWriter, r *http.Request) {
	project, err := a.store.PlanningProject(r.Context(), r.PathValue("project"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, project)
}

func (a *API) savePlanningProject(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input SavePlanningProjectRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	project, err := a.store.UpsertPlanningProject(r.Context(), r.PathValue("project"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_project", project.Project, "saved")
	writeJSON(w, http.StatusOK, project)
}

func (a *API) getPlanningCheckpoint(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	checkpoint, err := a.store.PlanningCheckpoint(r.Context(), r.PathValue("project"), number)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, checkpoint)
}

func (a *API) savePlanningCheckpoint(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input SavePlanningCheckpointRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	checkpoint, err := a.store.UpsertPlanningCheckpoint(r.Context(), r.PathValue("project"), number, input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_checkpoint", planningCheckpointKey(checkpoint), "saved")
	writeJSON(w, http.StatusOK, checkpoint)
}

func (a *API) insertPlanningCheckpoint(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input InsertPlanningCheckpointRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	checkpoint, err := a.store.InsertPlanningCheckpoint(r.Context(), r.PathValue("project"), number, input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_checkpoint", planningCheckpointKey(checkpoint), "inserted")
	writeJSON(w, http.StatusOK, checkpoint)
}

func (a *API) savePlanningAnswers(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input SavePlanningAnswersRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	checkpoint, err := a.store.SavePlanningAnswers(r.Context(), r.PathValue("project"), number, input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_checkpoint", planningCheckpointKey(checkpoint), "answered")
	writeJSON(w, http.StatusOK, checkpoint)
}

func (a *API) setPlanningStatus(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input PlanningStatusRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	checkpoint, err := a.store.TransitionPlanningCheckpoint(
		r.Context(), r.PathValue("project"), number, input.Status,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_checkpoint", planningCheckpointKey(checkpoint), checkpoint.Status)
	writeJSON(w, http.StatusOK, checkpoint)
}

func (a *API) savePlanningPebbles(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input SavePlanningPebblesRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	checkpoint, err := a.store.ReplacePlanningPebbles(r.Context(), r.PathValue("project"), number, input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_checkpoint", planningCheckpointKey(checkpoint), "split",
		"pebbles", len(checkpoint.Pebbles), "stones", len(checkpoint.Stones))
	writeJSON(w, http.StatusOK, checkpoint)
}

func (a *API) markPlanningPebbleBuilt(w http.ResponseWriter, r *http.Request) {
	number, ok := planningCheckpointNumber(w, r)
	if !ok {
		return
	}
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input MarkPlanningPebbleBuiltRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	pebble, err := a.store.MarkPlanningPebbleBuilt(
		r.Context(), r.PathValue("project"), number, r.PathValue("slug"), input,
	)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("planning_pebble",
		r.PathValue("project")+"/"+strconv.Itoa(number)+"/"+pebble.Slug, "built")
	writeJSON(w, http.StatusOK, pebble)
}

// planningCheckpointNumber parses the {number} path segment. A non-numeric
// segment is refused here rather than reaching the store, because the store's
// own bound answers "which numbers exist" and this answers "is this a number
// at all"; the two failures deserve the same code and the same message, so
// both say invalid_planning_number.
func planningCheckpointNumber(w http.ResponseWriter, r *http.Request) (int, bool) {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		writeError(w, invalid("invalid_planning_number",
			"a checkpoint number is between 1 and 999"))
		return 0, false
	}
	return number, true
}

func planningCheckpointKey(checkpoint PlanningCheckpoint) string {
	return checkpoint.Project + "/" + strconv.Itoa(checkpoint.Number)
}
