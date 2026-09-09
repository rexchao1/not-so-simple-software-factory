package controlplane

import (
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) listExecutionProfiles(w http.ResponseWriter, r *http.Request) {
	profiles, err := a.store.ExecutionProfiles(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profiles)
}

func (a *API) createExecutionProfile(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SaveExecutionProfileRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	profile, err := a.store.CreateExecutionProfile(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("execution_profile", profile.ID, "created")
	writeJSON(w, http.StatusCreated, profile)
}

func (a *API) getExecutionProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := a.store.ExecutionProfile(r.Context(), r.PathValue("profile_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (a *API) updateExecutionProfile(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.SaveExecutionProfileRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	profile, err := a.store.UpdateExecutionProfile(r.Context(), r.PathValue("profile_id"), input)
	if err != nil {
		writeError(w, err)
		return
	}
	a.logStateChange("execution_profile", profile.ID, "updated")
	writeJSON(w, http.StatusOK, profile)
}
