package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/owainlewis/factory/internal/protocol"
)

// planningClient is the loopback operator API as the light-factory skill's
// scripts see it: one handler, JSON in, JSON out.
type planningClient struct {
	t       *testing.T
	handler http.Handler
}

func newPlanningClient(t *testing.T) (*planningClient, *Store) {
	t.Helper()
	store := newTestStore(t)
	return &planningClient{
		t:       t,
		handler: NewHandler(store, slog.New(slog.NewTextHandler(io.Discard, nil))),
	}, store
}

func (c *planningClient) do(method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request := httptest.NewRequestWithContext(
		context.Background(), method, "http://localhost"+path, reader,
	)
	request.Host = "localhost"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	c.handler.ServeHTTP(response, request)
	return response
}

// ok runs a request that must succeed and decodes the checkpoint it returns.
func (c *planningClient) checkpoint(method, path string, body any) PlanningCheckpoint {
	c.t.Helper()
	response := c.do(method, path, body)
	if response.Code != http.StatusOK {
		c.t.Fatalf("%s %s = %d: %s", method, path, response.Code, response.Body.String())
	}
	var checkpoint PlanningCheckpoint
	if err := json.Unmarshal(response.Body.Bytes(), &checkpoint); err != nil {
		c.t.Fatalf("decode %s %s: %v", method, path, err)
	}
	return checkpoint
}

// requireAPIError asserts the response carries the repo's error envelope with
// one stable code, which is the whole contract a script can act on.
func requireAPIError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d: %s", response.Code, status, response.Body.String())
	}
	var body protocol.ErrorBody
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q (message %q)", body.Error.Code, code, body.Error.Message)
	}
	if strings.TrimSpace(body.Error.Message) == "" {
		t.Error("an error envelope carries a message a human can read")
	}
}

// The whole chain the light factory walks, over HTTP, in order: save a
// project, write a checkpoint, save answers, freeze it, cut pebbles, read it
// back, and see it on the roadmap.
func TestPlanningAPIWalksTheWholeChain(t *testing.T) {
	client, _ := newPlanningClient(t)

	projectResponse := client.do(http.MethodPut, "/api/v1/planning/projects/payer", SavePlanningProjectRequest{
		Title:     "a new payer onboards itself",
		Statement: "Give it a new payer portal and it works, unattended.",
		Route:     "# Route: a new payer onboards itself\n\n## Checkpoints\n1. Bring one up live\n",
	})
	if projectResponse.Code != http.StatusOK {
		t.Fatalf("save project = %d: %s", projectResponse.Code, projectResponse.Body.String())
	}
	var project PlanningProject
	if err := json.Unmarshal(projectResponse.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	if project.Project != "payer" || project.Title != "a new payer onboards itself" {
		t.Fatalf("saved project = %+v", project)
	}

	checkpoint := client.checkpoint(http.MethodPut, "/api/v1/planning/projects/payer/checkpoints/1",
		SavePlanningCheckpointRequest{
			Title:   "One payer brings itself up live",
			Summary: "the daily ledger allows 20 logins",
			Body:    "# Checkpoint 1: One payer brings itself up live\n\n## Slice\nOne payer.\n",
		})
	if checkpoint.Status != "planned" || checkpoint.Number != 1 {
		t.Fatalf("saved checkpoint = %+v", checkpoint)
	}

	checkpoint = client.checkpoint(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/1/status", PlanningStatusRequest{Status: "review"})
	if checkpoint.Status != "review" {
		t.Fatalf("status = %q, want review", checkpoint.Status)
	}

	checkpoint = client.checkpoint(http.MethodPut,
		"/api/v1/planning/projects/payer/checkpoints/1/answers",
		SavePlanningAnswersRequest{Answers: "Q1: rotate weekly. Q2: no SSO."})
	if checkpoint.Answers != "Q1: rotate weekly. Q2: no SSO." {
		t.Fatalf("answers = %q", checkpoint.Answers)
	}

	checkpoint = client.checkpoint(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/1/status", PlanningStatusRequest{Status: "frozen"})
	if checkpoint.Status != "frozen" || checkpoint.FrozenAt == nil {
		t.Fatalf("frozen checkpoint = %+v", checkpoint)
	}

	checkpoint = client.checkpoint(http.MethodPut,
		"/api/v1/planning/projects/payer/checkpoints/1/pebbles", SavePlanningPebblesRequest{
			Stones: []PlanningStone{{ID: "b1", Title: "Bring it up", Statement: "It comes up."}},
			Pebbles: []PlanningPebble{
				{Slug: "01-driver", Title: "Write the driver", StoneID: "b1", Body: "## Write the driver\n"},
				{Slug: "02-publish", Title: "Publish it", StoneID: "b1", Body: "## Publish it\n"},
			},
		})
	if len(checkpoint.Pebbles) != 2 || len(checkpoint.Stones) != 1 {
		t.Fatalf("split = %+v", checkpoint)
	}

	read := client.checkpoint(http.MethodGet, "/api/v1/planning/projects/payer/checkpoints/1", nil)
	if read.Status != "frozen" || len(read.Pebbles) != 2 || read.Answers == "" {
		t.Fatalf("read back = %+v", read)
	}

	listResponse := client.do(http.MethodGet, "/api/v1/planning/projects", nil)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", listResponse.Code, listResponse.Body.String())
	}
	var list struct {
		Projects []PlanningProject `json:"projects"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 || list.Projects[0].Checkpoints != 1 {
		t.Fatalf("list = %+v", list.Projects)
	}

	getResponse := client.do(http.MethodGet, "/api/v1/planning/projects/payer", nil)
	if getResponse.Code != http.StatusOK {
		t.Fatalf("get project = %d: %s", getResponse.Code, getResponse.Body.String())
	}

	// The roadmap the Planning page reads is now this chain's output.
	roadmapResponse := client.do(http.MethodGet, "/api/v1/roadmap", nil)
	if roadmapResponse.Code != http.StatusOK {
		t.Fatalf("roadmap = %d: %s", roadmapResponse.Code, roadmapResponse.Body.String())
	}
	var roadmap Roadmap
	if err := json.Unmarshal(roadmapResponse.Body.Bytes(), &roadmap); err != nil {
		t.Fatal(err)
	}
	if !roadmap.Configured || len(roadmap.Projects) != 1 {
		t.Fatalf("roadmap = %+v", roadmap)
	}
	built := roadmap.Projects[0].Checkpoints[0]
	if len(built.Pebbles) != 2 || len(built.Stones) != 1 {
		t.Fatalf("roadmap checkpoint = %+v", built)
	}
}

// Every refusal a script can provoke reaches it as the same envelope with a
// code it can branch on, at the status the code means.
func TestPlanningAPIReportsEveryRefusalAsAStableCode(t *testing.T) {
	client, store := newPlanningClient(t)
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")

	requireAPIError(t, client.do(http.MethodGet, "/api/v1/planning/projects/missing", nil),
		http.StatusNotFound, "not_found")
	requireAPIError(t, client.do(http.MethodGet, "/api/v1/planning/projects/payer/checkpoints/9", nil),
		http.StatusNotFound, "not_found")
	requireAPIError(t, client.do(http.MethodGet, "/api/v1/planning/projects/payer/checkpoints/two", nil),
		http.StatusBadRequest, "invalid_planning_number")
	requireAPIError(t, client.do(http.MethodPut, "/api/v1/planning/projects/Not%20A%20Slug",
		SavePlanningProjectRequest{}), http.StatusBadRequest, "invalid_planning_project")
	requireAPIError(t, client.do(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/1/status", PlanningStatusRequest{Status: "drafting"}),
		http.StatusBadRequest, "invalid_planning_status")
	requireAPIError(t, client.do(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/1/status", PlanningStatusRequest{Status: "built"}),
		http.StatusConflict, "planning_transition_not_allowed")
	requireAPIError(t, client.do(http.MethodPut,
		"/api/v1/planning/projects/payer/checkpoints/1/pebbles", SavePlanningPebblesRequest{}),
		http.StatusConflict, "planning_checkpoint_not_frozen")
	requireAPIError(t, client.do(http.MethodPut,
		"/api/v1/planning/projects/payer/checkpoints/1", SavePlanningCheckpointRequest{
			Body: strings.Repeat("x", planningMaxBodyBytes+1),
		}), http.StatusBadRequest, "invalid_planning_body")

	client.checkpoint(http.MethodPost, "/api/v1/planning/projects/payer/checkpoints/1/status",
		PlanningStatusRequest{Status: "review"})
	client.checkpoint(http.MethodPost, "/api/v1/planning/projects/payer/checkpoints/1/status",
		PlanningStatusRequest{Status: "frozen"})
	requireAPIError(t, client.do(http.MethodPut, "/api/v1/planning/projects/payer/checkpoints/1",
		SavePlanningCheckpointRequest{Title: "Rewritten"}),
		http.StatusConflict, "planning_checkpoint_frozen")
}

// Every write route is a mutation, so it takes the same guards as the rest of
// the operator API: JSON only, and no unknown fields quietly ignored.
func TestPlanningAPIWritesTakeTheSameGuardsAsEveryOtherMutation(t *testing.T) {
	client, store := newPlanningClient(t)
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")
	writes := []struct {
		method string
		path   string
	}{
		{http.MethodPut, "/api/v1/planning/projects/payer"},
		{http.MethodPut, "/api/v1/planning/projects/payer/checkpoints/1"},
		{http.MethodPost, "/api/v1/planning/projects/payer/checkpoints/2/insert"},
		{http.MethodPut, "/api/v1/planning/projects/payer/checkpoints/1/answers"},
		{http.MethodPost, "/api/v1/planning/projects/payer/checkpoints/1/status"},
		{http.MethodPut, "/api/v1/planning/projects/payer/checkpoints/1/pebbles"},
	}
	for _, write := range writes {
		t.Run(write.method+" "+write.path, func(t *testing.T) {
			request := httptest.NewRequestWithContext(context.Background(), write.method,
				"http://localhost"+write.path, strings.NewReader("{}"))
			request.Host = "localhost"
			request.Header.Set("Content-Type", "text/plain")
			response := httptest.NewRecorder()
			client.handler.ServeHTTP(response, request)
			requireAPIError(t, response, http.StatusUnsupportedMediaType, "json_required")

			requireAPIError(t, client.do(write.method, write.path,
				map[string]any{"nonsense": true}), http.StatusBadRequest, "malformed_json")
		})
	}
}

// The insert route is reachable over HTTP with the same shape the store
// tests already cover in depth: a clean insert shifts the tail up and
// replaces the route, and a checkpoint above it that is frozen or built
// refuses with the stable conflict code.
func TestPlanningAPIInsertsACheckpointAndRefusesADirtyShift(t *testing.T) {
	client, store := newPlanningClient(t)
	seedPlanningProject(t, store, "payer")
	seedPlanningCheckpoint(t, store, "payer", 1, "planned")

	checkpoint := client.checkpoint(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/2/insert", InsertPlanningCheckpointRequest{
			Title: "Found mid-route", Summary: "turned up late", Route: "# Route: revised\n",
		})
	if checkpoint.Number != 2 || checkpoint.Status != "planned" {
		t.Fatalf("inserted checkpoint = %+v", checkpoint)
	}
	project, err := store.PlanningProject(context.Background(), "payer")
	if err != nil {
		t.Fatal(err)
	}
	if project.Route != "# Route: revised" {
		t.Errorf("route = %q, want the insert's route", project.Route)
	}

	client.checkpoint(http.MethodPost, "/api/v1/planning/projects/payer/checkpoints/2/status",
		PlanningStatusRequest{Status: "review"})
	client.checkpoint(http.MethodPost, "/api/v1/planning/projects/payer/checkpoints/2/status",
		PlanningStatusRequest{Status: "frozen"})

	requireAPIError(t, client.do(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/1/insert", InsertPlanningCheckpointRequest{
			Title: "Should not land", Route: "# Rewritten\n",
		}), http.StatusConflict, "planning_insert_not_clean")

	requireAPIError(t, client.do(http.MethodPost,
		"/api/v1/planning/projects/payer/checkpoints/9/insert", InsertPlanningCheckpointRequest{
			Title: "Out of range", Route: "# Rewritten\n",
		}), http.StatusBadRequest, "planning_invalid_checkpoint_number")
}

// The planning API is operator-only, like the rest of the local listener: a
// request that did not come from loopback is refused before route handling.
func TestPlanningAPIIsLoopbackOnly(t *testing.T) {
	client, store := newPlanningClient(t)
	seedPlanningProject(t, store, "payer")
	request := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"http://example.test/api/v1/planning/projects", nil)
	request.Host = "example.test"
	response := httptest.NewRecorder()
	client.handler.ServeHTTP(response, request)
	if response.Code == http.StatusOK {
		t.Fatalf("a non-loopback host reached the planning API: %s", response.Body.String())
	}
}
