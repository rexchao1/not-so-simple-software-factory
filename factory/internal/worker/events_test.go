package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/owainlewis/factory/internal/protocol"
)

type wrappedEventPayload struct {
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated"`
}

func TestEventPayloadPassesThroughRuntimeJSON(t *testing.T) {
	text := `{"type":"result","result":"done"}`
	payload := eventPayload("stdout", text, false)
	if string(payload) != text {
		t.Fatalf("payload = %s, want the runtime line verbatim", payload)
	}
}

func TestEventPayloadWrapsOtherOutput(t *testing.T) {
	cases := []struct {
		name          string
		stream        string
		text          string
		truncated     bool
		wantTruncated bool
	}{
		{name: "stderr json", stream: "stderr", text: `{"ok":true}`},
		{name: "stdout plain text", stream: "stdout", text: "building the project"},
		{name: "stdout invalid json", stream: "stdout", text: `{"unterminated":`},
		{name: "stdout empty", stream: "stdout", text: ""},
		{name: "already truncated", stream: "stdout", text: `{"ok":true}`, truncated: true, wantTruncated: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			payload := eventPayload(testCase.stream, testCase.text, testCase.truncated)
			var wrapped wrappedEventPayload
			if err := json.Unmarshal(payload, &wrapped); err != nil {
				t.Fatalf("decode payload %s: %v", payload, err)
			}
			if wrapped.Stream != testCase.stream || wrapped.Text != testCase.text {
				t.Fatalf("payload = %+v, want stream %q and the original text", wrapped, testCase.stream)
			}
			if wrapped.Truncated != testCase.wantTruncated {
				t.Fatalf("payload truncated = %v, want %v", wrapped.Truncated, testCase.wantTruncated)
			}
		})
	}
}

func TestEventPayloadWrapsOversizedRuntimeJSON(t *testing.T) {
	text := `{"type":"result","result":"` + strings.Repeat("a", protocol.MaxEventBytes) + `"}`
	payload := eventPayload("stdout", text, false)
	if len(payload) > protocol.MaxEventBytes {
		t.Fatalf("payload is %d bytes, want at most %d", len(payload), protocol.MaxEventBytes)
	}
	var wrapped wrappedEventPayload
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !wrapped.Truncated || !strings.HasPrefix(text, wrapped.Text) {
		t.Fatalf("payload = truncated %v, %d bytes of text", wrapped.Truncated, len(wrapped.Text))
	}
}

func TestEventPayloadBinarySearchesEscapeHeavyText(t *testing.T) {
	// A NUL byte encodes as a six-character escape, so the first attempt at
	// MaxEventBytes-256 characters overflows the limit and the binary search
	// has to find the largest prefix that fits.
	text := strings.Repeat("\x00", protocol.MaxEventBytes)
	payload := eventPayload("stdout", text, false)
	if len(payload) > protocol.MaxEventBytes {
		t.Fatalf("payload is %d bytes, want at most %d", len(payload), protocol.MaxEventBytes)
	}
	var wrapped wrappedEventPayload
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !wrapped.Truncated {
		t.Fatal("escape-heavy payload was not marked truncated")
	}
	if len(wrapped.Text) == 0 {
		t.Fatal("escape-heavy payload kept no text")
	}
	if !strings.HasPrefix(text, wrapped.Text) {
		t.Fatal("escape-heavy payload kept text the runtime did not emit")
	}
	// The search must return the largest prefix that fits, so one more character
	// has to overflow.
	larger, err := json.Marshal(map[string]any{
		"stream": "stdout", "text": text[:len(wrapped.Text)+1], "truncated": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(larger) <= protocol.MaxEventBytes {
		t.Fatalf("payload kept %d characters but %d also fit", len(wrapped.Text), len(wrapped.Text)+1)
	}
}

func TestEventPayloadKeepsValidUTF8WhenTruncating(t *testing.T) {
	text := strings.Repeat("\x00", protocol.MaxEventBytes) + strings.Repeat("\U0001F600", 1024)
	payload := eventPayload("stderr", text, false)
	if len(payload) > protocol.MaxEventBytes {
		t.Fatalf("payload is %d bytes, want at most %d", len(payload), protocol.MaxEventBytes)
	}
	if !utf8.Valid(payload) {
		t.Fatal("payload is not valid UTF-8")
	}
	var wrapped wrappedEventPayload
	if err := json.Unmarshal(payload, &wrapped); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if !utf8.ValidString(wrapped.Text) {
		t.Fatal("payload text is not valid UTF-8")
	}
}

func TestEnqueueNumbersEventsAndStopsWhenTheQueueIsFull(t *testing.T) {
	sender := &eventSender{
		kind:  protocol.RuntimeCodex,
		input: make(chan protocol.AttemptEvent, 2),
		done:  make(chan struct{}),
	}

	sender.enqueue("stdout", "first", false)
	sender.enqueue("stderr", "second", false)
	sender.enqueue("stdout", "dropped", false)

	if !sender.disabled {
		t.Fatal("sender stayed enabled after its queue overflowed")
	}
	if sender.next != 2 {
		t.Fatalf("sender numbered %d events, want 2", sender.next)
	}
	if len(sender.input) != 2 {
		t.Fatalf("queue holds %d events, want 2", len(sender.input))
	}

	first := <-sender.input
	second := <-sender.input
	if first.Sequence != 0 || second.Sequence != 1 {
		t.Fatalf("sequences = %d, %d; want 0, 1", first.Sequence, second.Sequence)
	}
	if first.Kind != protocol.RuntimeCodex || second.Kind != protocol.RuntimeCodex {
		t.Fatalf("kinds = %q, %q", first.Kind, second.Kind)
	}
	var wrapped wrappedEventPayload
	if err := json.Unmarshal(second.Payload, &wrapped); err != nil {
		t.Fatal(err)
	}
	if wrapped.Stream != "stderr" || wrapped.Text != "second" {
		t.Fatalf("second payload = %+v", wrapped)
	}

	// A disabled sender drops later output instead of queueing it again.
	sender.enqueue("stdout", "after overflow", false)
	if len(sender.input) != 0 {
		t.Fatal("disabled sender queued more output")
	}
}

func TestCloseAndWaitGivesUpAfterItsTimeout(t *testing.T) {
	sender := &eventSender{input: make(chan protocol.AttemptEvent, 1), done: make(chan struct{})}

	start := time.Now()
	sender.closeAndWait(50 * time.Millisecond)
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("closeAndWait returned after %s, want it to wait for its timeout", elapsed)
	}
	if !sender.closed {
		t.Fatal("closeAndWait did not record the close")
	}

	// Closing twice must not close the input channel twice.
	sender.closeAndWait(time.Millisecond)

	sender.enqueue("stdout", "after close", false)
	if len(sender.input) != 0 {
		t.Fatal("closed sender queued more output")
	}
}

func TestEventSenderDeliversEventsInOrder(t *testing.T) {
	var mutex sync.Mutex
	var received []protocol.AttemptEvent
	var tokens []string
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input protocol.EventBatchRequest
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mutex.Lock()
		received = append(received, input.Events...)
		tokens = append(tokens, input.LeaseToken)
		paths = append(paths, r.URL.Path)
		mutex.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sender := newEventSender(context.Background(), newClient(server.URL, server.Client()),
		"attempt-1", "lease-token", protocol.RuntimeCodex)
	sender.enqueue("stdout", "one", false)
	sender.enqueue("stderr", "two", false)
	sender.closeAndWait(5 * time.Second)

	mutex.Lock()
	defer mutex.Unlock()
	if len(received) != 2 {
		t.Fatalf("control plane received %d events, want 2", len(received))
	}
	if received[0].Sequence != 0 || received[1].Sequence != 1 {
		t.Fatalf("sequences = %d, %d", received[0].Sequence, received[1].Sequence)
	}
	for index, token := range tokens {
		if token != "lease-token" {
			t.Fatalf("request %d carried lease token %q", index, token)
		}
		if paths[index] != "/api/v1/attempts/attempt-1/events" {
			t.Fatalf("request %d used path %q", index, paths[index])
		}
	}
}

func TestEventSenderRetriesServerFailures(t *testing.T) {
	var mutex sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		attempts++
		count := attempts
		mutex.Unlock()
		if count < 3 {
			http.Error(w, `{"error":{"code":"internal","message":"try again"}}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sender := newEventSender(context.Background(), newClient(server.URL, server.Client()),
		"attempt-1", "lease-token", protocol.RuntimeCodex)
	sender.enqueue("stdout", "one", false)
	sender.closeAndWait(10 * time.Second)

	mutex.Lock()
	defer mutex.Unlock()
	if attempts != 3 {
		t.Fatalf("sender made %d attempts, want 3", attempts)
	}
	sender.mutex.Lock()
	defer sender.mutex.Unlock()
	if sender.disabled {
		t.Fatal("sender disabled itself after a retry succeeded")
	}
}

func TestEventSenderStopsAfterClientError(t *testing.T) {
	var mutex sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		requests++
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"lease_lost","message":"lease no longer held"}}`))
	}))
	defer server.Close()

	sender := newEventSender(context.Background(), newClient(server.URL, server.Client()),
		"attempt-1", "lease-token", protocol.RuntimeCodex)
	sender.enqueue("stdout", "one", false)
	select {
	case <-sender.done:
	case <-time.After(5 * time.Second):
		t.Fatal("sender kept running after a client error")
	}

	sender.enqueue("stdout", "two", false)
	sender.closeAndWait(time.Second)

	mutex.Lock()
	defer mutex.Unlock()
	if requests != 1 {
		t.Fatalf("sender made %d requests, want 1", requests)
	}
}

func TestEventSenderStopsWhenItsContextIsCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"code":"internal","message":"try again"}}`, http.StatusInternalServerError)
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	sender := newEventSender(ctx, newClient(server.URL, server.Client()),
		"attempt-1", "lease-token", protocol.RuntimeCodex)
	sender.enqueue("stdout", "one", false)
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-sender.done:
	case <-time.After(5 * time.Second):
		t.Fatal("sender kept retrying after its context was cancelled")
	}
	sender.closeAndWait(time.Second)
}
