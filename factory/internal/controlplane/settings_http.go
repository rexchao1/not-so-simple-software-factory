package controlplane

import (
	"net/http"

	"github.com/owainlewis/factory/internal/protocol"
)

func (a *API) getStageDefaults(w http.ResponseWriter, r *http.Request) {
	defaults, err := a.store.StageDefaults(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, defaults)
}

func (a *API) getFactoryPause(w http.ResponseWriter, r *http.Request) {
	value, err := a.store.FactoryPause(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *API) setFactoryPause(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.FactoryPause
	if !decodeJSON(w, r, &input) {
		return
	}
	value, err := a.store.SetFactoryPause(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func (a *API) saveStageDefaults(w http.ResponseWriter, r *http.Request) {
	if !prepareMutation(w, r, protocol.MaxBodyBytes) {
		return
	}
	var input protocol.StageDefaults
	if !decodeJSON(w, r, &input) {
		return
	}
	saved, err := a.store.SaveStageDefaults(r.Context(), input)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}
