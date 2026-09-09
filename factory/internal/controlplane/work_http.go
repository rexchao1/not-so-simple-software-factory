package controlplane

import (
	"errors"
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) admitWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.AdmitWorkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	response, created, err := a.store.AdmitWork(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	a.logStateChange("work", response.RunID, "admitted")
	writeJSON(w, status, response)
}

func (a *API) answerWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.WorkAnswerRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	answer, err := a.store.AnswerWork(r.Context(), r.PathValue("work_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", answer.WorkID, "answered")
	writeJSON(w, http.StatusOK, answer)
}

func (a *API) approveWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ApproveWorkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	work, err := a.store.ApproveWork(r.Context(), r.PathValue("work_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", work.ID, "approved")
	writeJSON(w, http.StatusOK, work)
}

func (a *API) retryWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) || !decodeEmptyJSON(w, r) {
		return
	}
	work, err := a.store.Work(r.Context(), r.PathValue("work_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	detail, err := a.store.RetrySession(r.Context(), work.RunID, work.ID)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("work", work.ID, "queued")
	writeJSON(w, http.StatusOK, detail)
}

func (a *API) replaceWork(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.ReplaceWorkRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	if input.WorkID != "" && input.WorkID != r.PathValue("work_id") {
		writeError(w, invalid("work_id_mismatch", "body work_id must match the request path"))
		return
	}
	input.WorkID = r.PathValue("work_id")
	replacement, err := a.store.ReplaceWork(r.Context(), input)
	if err != nil {
		var service *ServiceError
		if errors.As(err, &service) && service.Status < http.StatusInternalServerError {
			writeJSON(w, service.Status, protocol.ErrorBody{Error: protocol.APIError{
				Code: service.Code, Message: service.Message,
				AdmissionResult: protocol.AdmissionRejectedBeforeCommit,
				RequestKey:      input.RequestKey,
			}})
			return
		}
		writeError(w, err)
		return
	}
	status := http.StatusOK
	if replacement.Result == protocol.AdmissionAdmitted {
		status = http.StatusCreated
		a.logStateChange("run", replacement.Run.Run.ID, string(replacement.Run.Run.State))
	}
	writeJSON(w, status, replacement)
}

// listWork serves the Work board: one row per Work item rather than one per
// Run, so that a repository tab shows only that repository's Work.
//
// GET /api/v1/work sits beside the existing POST /api/v1/work, which admits
// Work. Go's ServeMux routes on method as well as path, so the two do not
// collide.
func (a *API) listWork(w http.ResponseWriter, r *http.Request) {
	limit, err := pageLimit(r, defaultTaskPageSize, maxTaskPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	query := r.URL.Query()
	// The board is for build work, so a bare request excludes planning
	// passes by default. That default lives here, in the HTTP layer, rather
	// than in the store: roadmapWork calls WorkPage with an empty filter and
	// must keep seeing everything.
	planning := protocol.PlanningFilterExclude
	switch raw := query.Get("planning"); raw {
	case "":
		// absent: keep the default of excluding planning work
	case "all":
		planning = protocol.PlanningFilterAll
	case string(protocol.PlanningFilterExclude), string(protocol.PlanningFilterOnly):
		planning = protocol.PlanningFilter(raw)
	default:
		writeError(w, invalid("invalid_planning_filter", "planning must be exclude, only, or all"))
		return
	}
	filter := protocol.WorkFilter{
		RepositoryID: query.Get("repository_id"),
		RunID:        query.Get("run_id"),
		Planning:     planning,
	}
	// Repeated state parameters rather than one comma-separated value: the
	// board asks for several states at once, and the store validates each
	// against the known set.
	for _, state := range query["state"] {
		if state == "" {
			continue
		}
		filter.States = append(filter.States, protocol.SessionState(state))
	}
	page, err := a.store.WorkPage(r.Context(), filter, limit, query.Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

// getWork serves one Work item's detail page.
func (a *API) getWork(w http.ResponseWriter, r *http.Request) {
	detail, err := a.store.WorkDetail(r.Context(), r.PathValue("work_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}
