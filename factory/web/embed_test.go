package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedUIAndAPIRouting(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"path":"`+r.URL.Path+`"}`)
	})
	server := httptest.NewServer(NewHandler(api))
	t.Cleanup(server.Close)

	for _, route := range []string{"/", "/tasks/durable-task", "/workers/worker-a"} {
		response, err := server.Client().Get(server.URL + route)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `<div id="root"></div>`) {
			t.Fatalf("%s did not serve the SPA: status=%d body=%s", route, response.StatusCode, body)
		}
		if response.Header.Get("Content-Security-Policy") == "" {
			t.Fatalf("%s is missing browser security headers", route)
		}
	}

	response, err := server.Client().Get(server.URL + "/api/v1/tasks")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || string(body) != `{"path":"/api/v1/tasks"}` {
		t.Fatalf("API was not passed through: status=%d body=%s", response.StatusCode, body)
	}

	response, err = server.Client().Get(server.URL + "/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing asset status=%d, want 404", response.StatusCode)
	}
}
