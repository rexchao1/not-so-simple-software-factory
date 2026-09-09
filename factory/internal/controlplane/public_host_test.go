package controlplane

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Both guards below are what stands between a loopback-bound operator API and
// the rest of the world, and until this file neither had a test. The bug they
// are being fixed for: the cockpit served its HTML shell over `tailscale
// serve` and every API call inside it returned 403, which reads as the
// server being down.

const testPublicHost = "factory-host.example.ts.net"

func guardHandler(publicHost string, next http.Handler) http.Handler {
	api := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if publicHost != "" {
		WithPublicHost(publicHost)(api)
	}
	return api.requestLog(next, true)
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

func TestRequestHostGuardAcceptsLoopbackAndTheConfiguredPublicHost(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		publicHost string
		host       string
		wantStatus int
	}{
		{"loopback ipv4 with no public host", "", "127.0.0.1:7337", http.StatusOK},
		{"localhost with no public host", "", "localhost:7337", http.StatusOK},
		{"loopback ipv6 with no public host", "", "[::1]:7337", http.StatusOK},
		{"tailnet host is refused when none is configured", "", testPublicHost, http.StatusForbidden},
		{"tailnet host is accepted once configured", testPublicHost, testPublicHost, http.StatusOK},
		{"loopback still works alongside a public host", testPublicHost, "127.0.0.1:7337", http.StatusOK},
		{"a different host is still refused", testPublicHost, "evil.example.com", http.StatusForbidden},
		{"a suffix of the public host is not a match", testPublicHost, "evil-" + testPublicHost, http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://example/api/v1/repositories", nil)
			request.Host = testCase.host
			response := httptest.NewRecorder()
			guardHandler(testCase.publicHost, okHandler()).ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("Host %q with public host %q: got %d, want %d", testCase.host, testCase.publicHost, response.Code, testCase.wantStatus)
			}
		})
	}
}

func TestMutationOriginGuardAcceptsHTTPSOnlyForTheConfiguredPublicHost(t *testing.T) {
	mutation := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validateMutationOrigin(w, r) {
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	for _, testCase := range []struct {
		name       string
		publicHost string
		host       string
		origin     string
		wantStatus int
	}{
		{"no origin at all is allowed", "", "127.0.0.1:7337", "", http.StatusOK},
		{"same-origin http on loopback", "", "127.0.0.1:7337", "http://127.0.0.1:7337", http.StatusOK},
		{"https is refused when no public host is configured", "", "127.0.0.1:7337", "https://127.0.0.1:7337", http.StatusForbidden},
		{"https from the configured public host is allowed", testPublicHost, testPublicHost, "https://" + testPublicHost, http.StatusOK},
		{"cross-origin https is refused", testPublicHost, testPublicHost, "https://evil.example.com", http.StatusForbidden},
		{"cross-origin http is refused", "", "127.0.0.1:7337", "http://evil.example.com", http.StatusForbidden},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "http://example/api/v1/work", nil)
			request.Host = testCase.host
			if testCase.origin != "" {
				request.Header.Set("Origin", testCase.origin)
			}
			response := httptest.NewRecorder()
			guardHandler(testCase.publicHost, mutation).ServeHTTP(response, request)
			if response.Code != testCase.wantStatus {
				t.Fatalf("Origin %q Host %q public host %q: got %d, want %d", testCase.origin, testCase.host, testCase.publicHost, response.Code, testCase.wantStatus)
			}
		})
	}
}

// The default has to stay exactly as it was, because every deployment that does
// not set public_host is relying on it.
func TestPublicHostIsEmptyUnlessConfigured(t *testing.T) {
	api := &API{logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if api.publicHost != "" {
		t.Fatalf("a fresh API should trust no extra host, got %q", api.publicHost)
	}
	if hostMatchesPublic(testPublicHost, api.publicHost) {
		t.Fatal("an empty public host must never match")
	}
	WithPublicHost("  " + testPublicHost + ".  ")(api)
	if api.publicHost != testPublicHost {
		t.Fatalf("surrounding space and the root dot should be trimmed, got %q", api.publicHost)
	}
}
