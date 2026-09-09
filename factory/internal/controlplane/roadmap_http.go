package controlplane

import "net/http"

// getRoadmap serves the planning state the store now owns, joined against the
// factory's own runs so the page can say which pebble is being built right
// now. It is read-only in the strongest sense: this handler runs queries and
// nothing else, and the writes live on the /api/v1/planning routes, which a
// caller has to reach for deliberately.
func (a *API) getRoadmap(w http.ResponseWriter, r *http.Request) {
	roadmap, err := readRoadmap(r.Context(), a.store)
	if err != nil {
		a.logger.Error("read roadmap", "error", err)
		writeError(w, err)
		return
	}
	roadmapApplyWork(&roadmap, roadmapWorkIndex(r.Context(), a.store))
	roadmapSortWaiting(roadmap.Waiting)
	writeJSON(w, http.StatusOK, roadmap)
}
