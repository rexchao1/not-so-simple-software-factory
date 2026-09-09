package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

func TestRuntimeArgumentsApplyStageExecution(t *testing.T) {
	arguments, _, _, err := runtimeArguments(protocol.RuntimeClaudeCode, "/tmp/result",
		protocol.StageExecution{Model: "haiku", Effort: "low"}, false)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(arguments, " ")
	for _, wanted := range []string{
		"--model haiku", "--effort low", "--exclude-dynamic-system-prompt-sections",
	} {
		if !strings.Contains(joined, wanted) {
			t.Fatalf("arguments %q do not contain %q", joined, wanted)
		}
	}

	empty, _, _, err := runtimeArguments(protocol.RuntimeClaudeCode, "/tmp/result", protocol.StageExecution{}, false)
	if err != nil {
		t.Fatal(err)
	}
	emptyJoined := strings.Join(empty, " ")
	if strings.Contains(emptyJoined, "--model") || strings.Contains(emptyJoined, "--effort") {
		t.Fatalf("empty execution added a model or effort flag: %q", emptyJoined)
	}
}

func TestIsSupervisorCommand(t *testing.T) {
	cases := []struct {
		name      string
		arguments []string
		want      bool
	}{
		{name: "no arguments", arguments: nil, want: false},
		{name: "supervisor", arguments: []string{supervisorCommand}, want: true},
		{name: "supervisor with extra", arguments: []string{supervisorCommand, "extra"}, want: true},
		{name: "other command", arguments: []string{"run"}, want: false},
		{name: "not first", arguments: []string{"run", supervisorCommand}, want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := IsSupervisorCommand(testCase.arguments); got != testCase.want {
				t.Fatalf("IsSupervisorCommand(%q) = %v, want %v", testCase.arguments, got, testCase.want)
			}
		})
	}
}

func TestOutcomeCompletionSuppressesOnlyVerifiedSignalExit(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "kill -TERM $$")
	signalErr := command.Run()
	if !expectedOutcomeTerminationError(signalErr) {
		t.Fatalf("signal error was not recognized: %v", signalErr)
	}
	if err := supervisorCompletionError("outcome_reported", nil, nil, signalErr, nil, nil); err != nil {
		t.Fatalf("verified outcome termination returned %v", err)
	}

	stopErr := errors.New("stop failed")
	anchorErr := errors.New("anchor failed")
	readerErr := errors.New("readers failed")
	childErr := errors.New("child exited unexpectedly")
	for _, testCase := range []struct {
		name    string
		initial error
		stop    error
		child   error
		anchor  error
		readers error
		want    error
	}{
		{name: "initial", initial: childErr, want: childErr},
		{name: "stop", stop: stopErr, child: signalErr, want: stopErr},
		{name: "unexpected child", child: childErr, want: childErr},
		{name: "anchor", child: signalErr, anchor: anchorErr, want: anchorErr},
		{name: "readers", child: signalErr, readers: readerErr, want: readerErr},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := supervisorCompletionError(
				"outcome_reported", testCase.initial, testCase.stop, testCase.child,
				testCase.anchor, testCase.readers,
			)
			if !errors.Is(err, testCase.want) {
				t.Fatalf("completion error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestOutcomeCompletionForgivesAGracefulExitAfterTheOutcome(t *testing.T) {
	// Gap 7. A harness that installs a SIGTERM handler and exits cleanly reports
	// a normal exit status, not signal death. Claude Code does exactly this and
	// exits 143. Before this was fixed the guard forgave only ExitCode() == -1,
	// so the well behaved harness had its already durable outcome rewritten to
	// supervisor_error, the stage was completed failed with "exit status 143",
	// and CompleteAttempt then refused the attempt with pipeline_incomplete.
	//
	// The perverse part, and the reason this test exists: a harness that ignored
	// the signal and was SIGKILLed passed, while one that shut down cleanly did
	// not.
	graceful := exec.Command("/bin/sh", "-c", "trap 'exit 143' TERM; kill -TERM $$; sleep 5")
	gracefulErr := graceful.Run()

	var exitErr *exec.ExitError
	if !errors.As(gracefulErr, &exitErr) {
		t.Fatalf("expected a wait status, got %v", gracefulErr)
	}
	if got := exitErr.ExitCode(); got != 143 {
		t.Fatalf("graceful handler exit code = %d, want 143", got)
	}

	if err := supervisorCompletionError("outcome_reported", nil, nil, gracefulErr, nil, nil); err != nil {
		t.Fatalf("a graceful exit after a reported outcome was treated as a failure: %v", err)
	}

	// The same exit outside the outcome path stays terminal, because then it is
	// the child dying on its own rather than shutting down when asked.
	if err := supervisorCompletionError("exited", nil, nil, gracefulErr, nil, nil); err == nil {
		t.Fatal("a non-zero exit outside outcome_reported must remain a failure")
	}
}

func TestOutcomeReportedDefersToPendingLeaseLoss(t *testing.T) {
	leaseTimer := make(chan time.Time, 1)
	leaseTimer <- time.Now()
	if got := supervisorOutcomeReason(leaseTimer); got != "lease_lost" {
		t.Fatalf("pending lease timer outcome = %q", got)
	}
	if got := supervisorOutcomeReason(make(chan time.Time)); got != "outcome_reported" {
		t.Fatalf("active lease outcome = %q", got)
	}
}

func TestParseControlCommand(t *testing.T) {
	cases := []struct {
		name         string
		command      string
		wantAction   string
		wantDuration time.Duration
		wantError    bool
	}{
		{name: "start", command: "start", wantAction: "start"},
		{name: "cancel", command: "cancel", wantAction: "cancel"},
		{name: "lease lost", command: "lease_lost", wantAction: "lease_lost"},
		{name: "fail", command: "fail", wantAction: "fail"},
		{name: "timeout", command: "timeout", wantAction: "timeout"},
		{name: "outcome reported", command: "outcome_reported", wantAction: "outcome_reported"},
		{name: "surrounding space", command: "  cancel  ", wantAction: "cancel"},
		{name: "renew", command: "renew 1500", wantAction: "renew", wantDuration: 1500 * time.Millisecond},
		{name: "renew minimum", command: "renew 1", wantAction: "renew", wantDuration: time.Millisecond},
		{
			name:         "renew maximum",
			command:      "renew " + itoa(protocol.LeaseDuration.Milliseconds()),
			wantAction:   "renew",
			wantDuration: protocol.LeaseDuration,
		},
		{name: "renew above maximum", command: "renew " + itoa(protocol.LeaseDuration.Milliseconds()+1), wantError: true},
		{name: "renew zero", command: "renew 0", wantError: true},
		{name: "renew negative", command: "renew -1", wantError: true},
		{name: "renew not a number", command: "renew soon", wantError: true},
		{name: "renew missing duration", command: "renew", wantError: true},
		{name: "renew extra field", command: "renew 100 100", wantError: true},
		{name: "empty", command: "", wantError: true},
		{name: "unknown", command: "explode", wantError: true},
		{name: "start with argument", command: "start now", wantError: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			action, duration, err := parseControlCommand(testCase.command)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("parseControlCommand(%q) returned no error", testCase.command)
				}
				if action != "" || duration != 0 {
					t.Fatalf("parseControlCommand(%q) = %q, %s on error", testCase.command, action, duration)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseControlCommand(%q): %v", testCase.command, err)
			}
			if action != testCase.wantAction || duration != testCase.wantDuration {
				t.Fatalf("parseControlCommand(%q) = %q, %s; want %q, %s",
					testCase.command, action, duration, testCase.wantAction, testCase.wantDuration)
			}
		})
	}
}

func TestLeaseStopDelay(t *testing.T) {
	cases := []struct {
		name      string
		remaining time.Duration
		want      time.Duration
	}{
		{name: "full lease", remaining: protocol.LeaseDuration, want: protocol.LeaseDuration - terminationGrace},
		{name: "above grace", remaining: terminationGrace + 2*time.Millisecond, want: 2 * time.Millisecond},
		{name: "one millisecond above grace", remaining: terminationGrace + time.Millisecond, want: time.Millisecond},
		{name: "at grace", remaining: terminationGrace, want: time.Millisecond},
		{name: "below grace", remaining: time.Second, want: time.Millisecond},
		{name: "zero", remaining: 0, want: time.Millisecond},
		{name: "negative", remaining: -time.Minute, want: time.Millisecond},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := leaseStopDelay(testCase.remaining); got != testCase.want {
				t.Fatalf("leaseStopDelay(%s) = %s, want %s", testCase.remaining, got, testCase.want)
			}
		})
	}
}

func TestResetTimerDrainsExpiredTimer(t *testing.T) {
	timer := time.NewTimer(time.Millisecond)
	defer timer.Stop()
	<-time.After(20 * time.Millisecond)

	start := time.Now()
	resetTimer(timer, 80*time.Millisecond)
	<-timer.C
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("reset timer fired after %s, want at least 40ms", elapsed)
	}
}

func TestResetTimerReschedulesPendingTimer(t *testing.T) {
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	resetTimer(timer, 10*time.Millisecond)
	select {
	case <-timer.C:
	case <-time.After(2 * time.Second):
		t.Fatal("reset timer never fired")
	}
}

func TestBoundedText(t *testing.T) {
	cases := []struct {
		name  string
		value string
		limit int
		want  string
	}{
		{name: "below limit", value: "hello", limit: 10, want: "hello"},
		{name: "at limit", value: "hello", limit: 5, want: "hello"},
		{name: "above limit", value: "hello", limit: 3, want: "hel"},
		{name: "zero limit", value: "hello", limit: 0, want: ""},
		{name: "empty", value: "", limit: 0, want: ""},
		{name: "splits multi byte rune", value: "héllo", limit: 2, want: "h"},
		{name: "keeps whole multi byte rune", value: "héllo", limit: 3, want: "hé"},
		{name: "splits four byte rune", value: "😀", limit: 3, want: ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := boundedText(testCase.value, testCase.limit); got != testCase.want {
				t.Fatalf("boundedText(%q, %d) = %q, want %q", testCase.value, testCase.limit, got, testCase.want)
			}
		})
	}
}

func TestReadBoundedText(t *testing.T) {
	directory := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		text, truncated, err := readBoundedText(filepath.Join(directory, "absent"), 128)
		if err != nil || text != "" || truncated {
			t.Fatalf("readBoundedText = %q, %v, %v", text, truncated, err)
		}
	})

	t.Run("within limit", func(t *testing.T) {
		path := filepath.Join(directory, "small")
		writeTestFile(t, path, "result body")
		text, truncated, err := readBoundedText(path, 128)
		if err != nil {
			t.Fatal(err)
		}
		if text != "result body" || truncated {
			t.Fatalf("readBoundedText = %q, %v", text, truncated)
		}
	})

	t.Run("at limit", func(t *testing.T) {
		path := filepath.Join(directory, "exact")
		writeTestFile(t, path, "abcd")
		text, truncated, err := readBoundedText(path, 4)
		if err != nil {
			t.Fatal(err)
		}
		if text != "abcd" || truncated {
			t.Fatalf("readBoundedText = %q, %v", text, truncated)
		}
	})

	t.Run("above limit", func(t *testing.T) {
		path := filepath.Join(directory, "large")
		writeTestFile(t, path, strings.Repeat("a", 100))
		text, truncated, err := readBoundedText(path, 10)
		if err != nil {
			t.Fatal(err)
		}
		if text != strings.Repeat("a", 10) || !truncated {
			t.Fatalf("readBoundedText = %q, %v", text, truncated)
		}
	})

	t.Run("truncation splits a multi byte rune", func(t *testing.T) {
		// readBoundedText cuts the body to the limit before calling boundedText,
		// which only trims when the value is longer than the limit, so the tail
		// rune can be split. Tracked in #322; recorded here as current behaviour
		// so a fix has to update this expectation deliberately.
		path := filepath.Join(directory, "utf8")
		writeTestFile(t, path, "aa\U0001F600bb")
		text, truncated, err := readBoundedText(path, 3)
		if err != nil {
			t.Fatal(err)
		}
		if text != "aa\xf0" || !truncated {
			t.Fatalf("readBoundedText = %q, %v", text, truncated)
		}
	})

	t.Run("unreadable path", func(t *testing.T) {
		_, _, err := readBoundedText(directory, 10)
		if err == nil {
			t.Fatal("readBoundedText on a directory returned no error")
		}
	})
}

func TestExitCode(t *testing.T) {
	if got := exitCode(nil); got != 0 {
		t.Fatalf("exitCode(nil) = %d, want 0", got)
	}
	if got := exitCode(errors.New("boom")); got != -1 {
		t.Fatalf("exitCode(generic) = %d, want -1", got)
	}
	err := exec.Command("/bin/sh", "-c", "exit 7").Run()
	if got := exitCode(err); got != 7 {
		t.Fatalf("exitCode(exit 7) = %d, want 7", got)
	}
	if got := exitCode(errors.Join(errors.New("wrapped"), err)); got != 7 {
		t.Fatalf("exitCode(joined exit 7) = %d, want 7", got)
	}
}

func TestTailBuffer(t *testing.T) {
	buffer := &tailBuffer{limit: 8}
	if got := buffer.String(); got != "" {
		t.Fatalf("empty tail buffer = %q", got)
	}
	if _, err := buffer.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "abc" {
		t.Fatalf("tail buffer = %q, want %q", got, "abc")
	}
	if _, err := buffer.Write([]byte("defghijkl")); err != nil {
		t.Fatal(err)
	}
	if got := buffer.String(); got != "efghijkl" {
		t.Fatalf("tail buffer = %q, want %q", got, "efghijkl")
	}
}

func TestPlainResultCapture(t *testing.T) {
	capture := &plainResultCapture{}
	capture.capture([]byte("first"), false)
	capture.capture([]byte(" line"), true)
	capture.capture([]byte("second"), true)
	if capture.result == nil {
		t.Fatal("plain capture recorded no result")
	}
	if got := capture.result.String(); got != "first line\nsecond\n" {
		t.Fatalf("plain capture = %q", got)
	}
	if capture.result.Truncated() {
		t.Fatal("plain capture reported truncation for a small result")
	}
}

func TestPlainResultCaptureTruncates(t *testing.T) {
	capture := &plainResultCapture{}
	capture.capture(bytes.Repeat([]byte("a"), protocol.MaxResultBytes+64), true)
	if !capture.result.Truncated() {
		t.Fatal("plain capture did not report truncation")
	}
	if got := len(capture.result.String()); got != protocol.MaxResultBytes {
		t.Fatalf("plain capture kept %d bytes, want %d", got, protocol.MaxResultBytes)
	}
}

func TestClaudeResultCapture(t *testing.T) {
	cases := []struct {
		name          string
		lines         []string
		wantFound     bool
		wantResult    string
		wantIsError   bool
		wantTruncated bool
		wantCost      *float64
		wantUsage     *protocol.Usage
		wantModels    map[string]protocol.ModelUsage
	}{
		{
			name:       "terminal result",
			lines:      []string{`{"type":"result","result":"all done","is_error":false}`},
			wantFound:  true,
			wantResult: "all done",
		},
		{
			name:        "terminal error",
			lines:       []string{`{"type":"result","result":"it failed","is_error":true}`},
			wantFound:   true,
			wantResult:  "it failed",
			wantIsError: true,
		},
		{
			name:      "non terminal event",
			lines:     []string{`{"type":"assistant","result":"ignored"}`},
			wantFound: false,
		},
		{
			name:      "invalid json",
			lines:     []string{`{"type":"result","result":`},
			wantFound: false,
		},
		{
			name:      "unterminated result string",
			lines:     []string{`{"type":"result","result":"open`},
			wantFound: false,
		},
		{
			name:       "escape sequences",
			lines:      []string{`{"type":"result","result":"a\nb\tc\\d\"e\/f\u0041"}`},
			wantFound:  true,
			wantResult: "a\nb\tc\\d\"e/fA",
		},
		{
			name:       "control escapes",
			lines:      []string{`{"type":"result","result":"a\bb\fc\rd"}`},
			wantFound:  true,
			wantResult: "a\bb\fc\rd",
		},
		{
			name:       "surrogate pair",
			lines:      []string{`{"type":"result","result":"\ud83d\ude00"}`},
			wantFound:  true,
			wantResult: "\U0001F600",
		},
		{
			name:       "lone high surrogate",
			lines:      []string{`{"type":"result","result":"\ud83dx"}`},
			wantFound:  true,
			wantResult: "�x",
		},
		{
			name:       "lone low surrogate",
			lines:      []string{`{"type":"result","result":"\udc00x"}`},
			wantFound:  true,
			wantResult: "�x",
		},
		{
			name:       "nested result key is ignored",
			lines:      []string{`{"type":"result","payload":{"result":"inner"},"result":"outer"}`},
			wantFound:  true,
			wantResult: "outer",
		},
		{
			name:       "result key with escaped name",
			lines:      []string{`{"type":"result","\u0072esult":"escaped key"}`},
			wantFound:  true,
			wantResult: "escaped key",
		},
		{
			name:       "non string result value",
			lines:      []string{`{"type":"result","result":null}`},
			wantFound:  true,
			wantResult: "",
		},
		{
			name: "last result line wins",
			lines: []string{
				`{"type":"system","subtype":"init"}`,
				`{"type":"result","result":"first"}`,
				`{"type":"result","result":"second"}`,
			},
			wantFound:  true,
			wantResult: "second",
		},
		{
			name:      "invalid escape",
			lines:     []string{`{"type":"result","result":"a\qb"}`},
			wantFound: false,
		},
		{
			name:      "invalid unicode escape",
			lines:     []string{`{"type":"result","result":"a\u00zz"}`},
			wantFound: false,
		},
		{
			name:      "raw control byte in result",
			lines:     []string{"{\"type\":\"result\",\"result\":\"a\x01b\"}"},
			wantFound: false,
		},
		{
			name: "cost usage and per model breakdown",
			lines: []string{`{"type":"result","result":"all done","is_error":false,"total_cost_usd":0.4275,` +
				`"usage":{"input_tokens":11,"cache_creation_input_tokens":22,` +
				`"cache_read_input_tokens":33,"output_tokens":44},"modelUsage":{` +
				`"claude-opus-4":{"inputTokens":1,"outputTokens":2,"cacheReadInputTokens":3,` +
				`"cacheCreationInputTokens":4,"costUSD":0.3275,"contextWindow":200000,"costBasis":"api"},` +
				`"claude-haiku-4":{"inputTokens":5,"outputTokens":6,"cacheReadInputTokens":7,` +
				`"cacheCreationInputTokens":8,"costUSD":0.1}}}`},
			wantFound:  true,
			wantResult: "all done",
			wantCost:   costPointer(0.4275),
			wantUsage: &protocol.Usage{
				InputTokens: 11, CacheCreationInputTokens: 22, CacheReadInputTokens: 33, OutputTokens: 44,
			},
			wantModels: map[string]protocol.ModelUsage{
				"claude-opus-4": {
					InputTokens: 1, CacheCreationInputTokens: 4,
					CacheReadInputTokens: 3, OutputTokens: 2, CostUSD: 0.3275,
				},
				"claude-haiku-4": {
					InputTokens: 5, CacheCreationInputTokens: 8,
					CacheReadInputTokens: 7, OutputTokens: 6, CostUSD: 0.1,
				},
			},
		},
		{
			name:       "result event reporting no cost at all",
			lines:      []string{`{"type":"result","result":"all done","is_error":false}`},
			wantFound:  true,
			wantResult: "all done",
		},
		{
			name: "usage without cost or breakdown",
			lines: []string{`{"type":"result","result":"all done","usage":{"input_tokens":11,` +
				`"cache_creation_input_tokens":22,"cache_read_input_tokens":33,"output_tokens":44}}`},
			wantFound:  true,
			wantResult: "all done",
			wantUsage: &protocol.Usage{
				InputTokens: 11, CacheCreationInputTokens: 22, CacheReadInputTokens: 33, OutputTokens: 44,
			},
		},
		{
			name: "malformed cost costs nothing but itself",
			lines: []string{`{"type":"result","result":"it failed","is_error":true,"total_cost_usd":"abc",` +
				`"usage":{"input_tokens":11,"cache_creation_input_tokens":22,"cache_read_input_tokens":33},` +
				`"modelUsage":{"claude-opus-4":{"inputTokens":1,"outputTokens":2,` +
				`"cacheReadInputTokens":3,"cacheCreationInputTokens":4}}}`},
			wantFound:   true,
			wantResult:  "it failed",
			wantIsError: true,
		},
		{
			name: "negative count rejects the whole usage",
			lines: []string{`{"type":"result","result":"all done","usage":{"input_tokens":-1,` +
				`"cache_creation_input_tokens":22,"cache_read_input_tokens":33,"output_tokens":44}}`},
			wantFound:  true,
			wantResult: "all done",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			capture := &claudeResultCapture{}
			for _, line := range testCase.lines {
				capture.capture([]byte(line), true)
			}
			if capture.found != testCase.wantFound {
				t.Fatalf("found = %v, want %v", capture.found, testCase.wantFound)
			}
			if capture.result != testCase.wantResult {
				t.Fatalf("result = %q, want %q", capture.result, testCase.wantResult)
			}
			if capture.isError != testCase.wantIsError {
				t.Fatalf("isError = %v, want %v", capture.isError, testCase.wantIsError)
			}
			if capture.truncated != testCase.wantTruncated {
				t.Fatalf("truncated = %v, want %v", capture.truncated, testCase.wantTruncated)
			}
			if !reflect.DeepEqual(capture.costUSD, testCase.wantCost) {
				t.Fatalf("costUSD = %s, want %s", formatCost(capture.costUSD), formatCost(testCase.wantCost))
			}
			if !reflect.DeepEqual(capture.usage, testCase.wantUsage) {
				t.Fatalf("usage = %+v, want %+v", capture.usage, testCase.wantUsage)
			}
			if !reflect.DeepEqual(capture.models, testCase.wantModels) {
				t.Fatalf("models = %+v, want %+v", capture.models, testCase.wantModels)
			}
		})
	}
}

func costPointer(value float64) *float64 {
	return &value
}

func formatCost(value *float64) string {
	if value == nil {
		return "nil"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}

func TestClaudeResultCaptureAcceptsFragments(t *testing.T) {
	line := `{"type":"result","result":"chunked 😀 output","is_error":false}`
	capture := &claudeResultCapture{}
	for index := 0; index < len(line); index += 3 {
		end := index + 3
		if end > len(line) {
			end = len(line)
		}
		capture.capture([]byte(line[index:end]), false)
	}
	capture.capture(nil, true)
	if !capture.found || capture.result != "chunked \U0001F600 output" {
		t.Fatalf("fragmented capture = %q, found %v", capture.result, capture.found)
	}
}

func TestClaudeResultCaptureTruncatesLargeResult(t *testing.T) {
	line := `{"type":"result","result":"` + strings.Repeat("a", protocol.MaxResultBytes+512) + `"}`
	capture := &claudeResultCapture{}
	capture.capture([]byte(line), true)
	if !capture.found {
		t.Fatal("large result was not captured")
	}
	if !capture.truncated {
		t.Fatal("large result was not reported as truncated")
	}
	if len(capture.result) != protocol.MaxResultBytes {
		t.Fatalf("captured %d bytes, want %d", len(capture.result), protocol.MaxResultBytes)
	}
}

func TestClaudeResultCaptureRejectsOversizedLine(t *testing.T) {
	line := `{"type":"result","padding":"` + strings.Repeat("a", maxSupervisorLineBytes) + `","result":"late"}`
	capture := &claudeResultCapture{}
	capture.capture([]byte(line), true)
	if capture.found {
		t.Fatalf("oversized line was captured as %q", capture.result)
	}
}

func TestHexDigit(t *testing.T) {
	cases := map[byte]struct {
		value byte
		ok    bool
	}{
		'0': {value: 0, ok: true},
		'9': {value: 9, ok: true},
		'a': {value: 10, ok: true},
		'f': {value: 15, ok: true},
		'A': {value: 10, ok: true},
		'F': {value: 15, ok: true},
		'g': {value: 0, ok: false},
		' ': {value: 0, ok: false},
	}
	for input, want := range cases {
		value, ok := hexDigit(input)
		if value != want.value || ok != want.ok {
			t.Fatalf("hexDigit(%q) = %d, %v; want %d, %v", input, value, ok, want.value, want.ok)
		}
	}
}

func TestStreamSupervisorOutput(t *testing.T) {
	output := &bytes.Buffer{}
	writer := &synchronizedEncoder{encoder: json.NewEncoder(output)}
	tail := &tailBuffer{limit: protocol.MaxErrorBytes}
	capture := &plainResultCapture{}

	streamSupervisorOutput(strings.NewReader("first\n\nsecond\nthird"), "stdout", writer, tail, capture.capture)

	messages := decodeSupervisorMessages(t, output.Bytes())
	if len(messages) != 3 {
		t.Fatalf("streamed %d messages, want 3: %+v", len(messages), messages)
	}
	wantText := []string{"first", "second", "third"}
	for index, message := range messages {
		if message.Type != "output" || message.Stream != "stdout" || message.Truncated {
			t.Fatalf("message %d = %+v", index, message)
		}
		if message.Text != wantText[index] {
			t.Fatalf("message %d text = %q, want %q", index, message.Text, wantText[index])
		}
	}
	if got := tail.String(); got != "first\nsecond\nthird\n" {
		t.Fatalf("tail = %q", got)
	}
	if got := capture.result.String(); got != "first\nsecond\nthird\n" {
		t.Fatalf("capture = %q", got)
	}
}

func TestStreamSupervisorOutputTruncatesLongLine(t *testing.T) {
	output := &bytes.Buffer{}
	writer := &synchronizedEncoder{encoder: json.NewEncoder(output)}
	line := strings.Repeat("a", maxSupervisorLineBytes+1024)

	streamSupervisorOutput(strings.NewReader(line+"\nshort\n"), "stderr", writer, nil, nil)

	messages := decodeSupervisorMessages(t, output.Bytes())
	if len(messages) != 2 {
		t.Fatalf("streamed %d messages, want 2", len(messages))
	}
	if !messages[0].Truncated || len(messages[0].Text) != maxSupervisorLineBytes {
		t.Fatalf("long line message truncated=%v length=%d", messages[0].Truncated, len(messages[0].Text))
	}
	if messages[1].Truncated || messages[1].Text != "short" {
		t.Fatalf("second message = %+v", messages[1])
	}
}

func TestStreamSupervisorOutputIgnoresEmptyStream(t *testing.T) {
	output := &bytes.Buffer{}
	writer := &synchronizedEncoder{encoder: json.NewEncoder(output)}
	streamSupervisorOutput(strings.NewReader(""), "stdout", writer, nil, nil)
	if output.Len() != 0 {
		t.Fatalf("empty stream produced %q", output.String())
	}
}

func TestRuntimeEnvironment(t *testing.T) {
	t.Setenv("FACTORY_RUN_ID", "stale-run")
	t.Setenv("FACTORY_SESSION_ID", "stale-session")
	t.Setenv("FACTORY_WORK_ID", "stale-work")
	t.Setenv("FACTORY_ATTEMPT_ID", "stale-attempt")
	t.Setenv("FACTORY_UPDATE_SOCKET", "/stale/socket")
	t.Setenv("FACTORY_UPDATE_TOKEN", "stale-token")
	t.Setenv("FACTORY_TEST_MARKER", "kept")

	withIdentity := runtimeEnvironment("run-1", "session-1", "attempt-1", "/private/update.sock", "update-token")
	if countEnvironment(withIdentity, "FACTORY_RUN_ID=") != 1 ||
		countEnvironment(withIdentity, "FACTORY_SESSION_ID=") != 1 {
		t.Fatalf("environment did not replace stale identity values: %v", identityValues(withIdentity))
	}
	if !containsEnvironment(withIdentity, "FACTORY_RUN_ID=run-1") ||
		!containsEnvironment(withIdentity, "FACTORY_SESSION_ID=session-1") ||
		!containsEnvironment(withIdentity, "FACTORY_WORK_ID=session-1") ||
		!containsEnvironment(withIdentity, "FACTORY_ATTEMPT_ID=attempt-1") ||
		!containsEnvironment(withIdentity, "FACTORY_UPDATE_SOCKET=/private/update.sock") ||
		!containsEnvironment(withIdentity, "FACTORY_UPDATE_TOKEN=update-token") {
		t.Fatalf("environment missing supplied identity: %v", identityValues(withIdentity))
	}
	if !containsEnvironment(withIdentity, "FACTORY_TEST_MARKER=kept") {
		t.Fatal("environment dropped unrelated variables")
	}

	withoutIdentity := runtimeEnvironment("", "", "", "", "")
	if countEnvironment(withoutIdentity, "FACTORY_RUN_ID=") != 0 ||
		countEnvironment(withoutIdentity, "FACTORY_SESSION_ID=") != 0 ||
		countEnvironment(withoutIdentity, "FACTORY_WORK_ID=") != 0 ||
		countEnvironment(withoutIdentity, "FACTORY_ATTEMPT_ID=") != 0 ||
		countEnvironment(withoutIdentity, "FACTORY_UPDATE_SOCKET=") != 0 ||
		countEnvironment(withoutIdentity, "FACTORY_UPDATE_TOKEN=") != 0 {
		t.Fatalf("environment kept identity values: %v", identityValues(withoutIdentity))
	}
}

func TestSupervisorCommandLine(t *testing.T) {
	got := supervisorCommandLine("/usr/local/bin/factory-worker")
	want := []string{"/usr/local/bin/factory-worker", supervisorCommand}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("supervisorCommandLine = %v, want %v", got, want)
	}
}

func TestResultPath(t *testing.T) {
	const attemptID = "123e4567-e89b-12d3-a456-426614174000"
	directory := t.TempDir()

	path, err := resultPath(directory, attemptID)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(directory, "tmp", attemptID+"-result"); path != want {
		t.Fatalf("resultPath = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("result file mode = %v, want 0600", info.Mode().Perm())
	}
	temporary, err := os.Stat(filepath.Join(directory, "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if temporary.Mode().Perm() != 0o700 {
		t.Fatalf("temporary directory mode = %v, want 0700", temporary.Mode().Perm())
	}

	if _, err := resultPath(directory, attemptID); err == nil {
		t.Fatal("resultPath reused an existing result file")
	}
	if _, err := resultPath(directory, "not-a-uuid"); err == nil {
		t.Fatal("resultPath accepted an invalid attempt ID")
	}

	symlinked := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(symlinked, "tmp")); err != nil {
		t.Fatal(err)
	}
	if _, err := resultPath(symlinked, attemptID); err == nil {
		t.Fatal("resultPath accepted a symlinked temporary directory")
	}

	file := filepath.Join(t.TempDir(), "data")
	writeTestFile(t, file, "not a directory")
	if _, err := resultPath(file, attemptID); err == nil {
		t.Fatal("resultPath accepted a file as the data directory")
	}
}

func TestSupervisorStartRequestAndSummary(t *testing.T) {
	process := &supervisorProcess{
		supervisorPID:      4242,
		supervisorIdentity: "supervisor-identity",
		processGroupID:     4243,
		groupIdentity:      "group-identity",
	}
	request := supervisorStartRequest(process, "lease-token")
	if request.LeaseToken != "lease-token" || request.ProcessIdentity != "supervisor-identity" {
		t.Fatalf("start request = %+v", request)
	}
	if request.SupervisorPID == nil || *request.SupervisorPID != 4242 {
		t.Fatalf("start request supervisor pid = %v", request.SupervisorPID)
	}
	if request.ProcessGroupID == nil || *request.ProcessGroupID != 4243 {
		t.Fatalf("start request process group = %v", request.ProcessGroupID)
	}
	if got := processSummary(process); got != "supervisor=4242 process_group=4243" {
		t.Fatalf("processSummary = %q", got)
	}
}

func TestSupervisorProcessControl(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	process := &supervisorProcess{control: writer}

	if err := process.send("cancel"); err != nil {
		t.Fatal(err)
	}
	if err := process.renew(time.Now().Add(protocol.LeaseDuration * 4)); err != nil {
		t.Fatal(err)
	}
	if err := process.renew(time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := process.renew(time.Now().Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if err := process.closeControl(); err != nil {
		t.Fatal(err)
	}
	if err := process.closeControl(); err != nil {
		t.Fatalf("second closeControl: %v", err)
	}
	if err := process.send("cancel"); err == nil {
		t.Fatal("send on a closed control pipe returned no error")
	}

	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	commands := strings.Split(strings.TrimSuffix(string(body), "\n"), "\n")
	if len(commands) != 4 {
		t.Fatalf("control pipe carried %d commands: %q", len(commands), commands)
	}
	if commands[0] != "cancel" {
		t.Fatalf("first command = %q", commands[0])
	}
	if want := "renew " + itoa(protocol.LeaseDuration.Milliseconds()); commands[1] != want {
		t.Fatalf("clamped renew = %q, want %q", commands[1], want)
	}
	if commands[2] != "lease_lost" {
		t.Fatalf("expired lease command = %q", commands[2])
	}
	// A sub-millisecond lease can expire between the caller reading the clock and
	// the supervisor process comparing it, so both outcomes are correct here.
	if commands[3] != "renew 1" && commands[3] != "lease_lost" {
		t.Fatalf("sub millisecond renew = %q, want \"renew 1\" or \"lease_lost\"", commands[3])
	}

	for _, command := range commands {
		if _, _, err := parseControlCommand(command); err != nil {
			t.Fatalf("supervisor cannot parse the command it is sent %q: %v", command, err)
		}
	}
}

func TestSupervisorProcessStoppedFlag(t *testing.T) {
	process := &supervisorProcess{}
	if process.isStopped() {
		t.Fatal("new supervisor process reported a verified stop")
	}
	process.markStopped()
	if !process.isStopped() {
		t.Fatal("markStopped did not record a verified stop")
	}
}

func TestUnverifiedSupervisorExitDoesNotMarkProcessStopped(t *testing.T) {
	process := newTestSupervisorProcess()
	process.messages <- supervisorMessage{
		Type: "exit", Reason: "supervisor_error", Error: "child did not reap", StopUnverified: true,
	}
	manager := &Manager{}
	message := manager.waitForSupervisorMessage(process)
	if message.Reason != "supervisor_error" {
		t.Fatalf("exit message = %#v", message)
	}
	if process.isStopped() {
		t.Fatal("unverified supervisor exit marked the process stopped")
	}
}

func TestAwaitRuntimeStartedConsumesSupervisorAcknowledgement(t *testing.T) {
	process := newTestSupervisorProcess()
	process.messages <- supervisorMessage{Type: "started"}
	if err := (&Manager{}).awaitRuntimeStarted(process); err != nil {
		t.Fatal(err)
	}
}

func TestAwaitReady(t *testing.T) {
	t.Run("valid readiness", func(t *testing.T) {
		process := newTestSupervisorProcess()
		process.messages <- supervisorMessage{
			Type: "ready", SupervisorPID: 11, ProcessIdentity: "supervisor",
			ProcessGroupID: 12, GroupIdentity: "group",
		}
		if err := process.awaitReady(context.Background()); err != nil {
			t.Fatal(err)
		}
		if process.supervisorPID != 11 || process.processGroupID != 12 ||
			process.supervisorIdentity != "supervisor" || process.groupIdentity != "group" {
			t.Fatalf("readiness fields = %+v", process)
		}
	})

	invalid := []struct {
		name    string
		message supervisorMessage
	}{
		{
			name:    "wrong message type",
			message: supervisorMessage{Type: "output", SupervisorPID: 11, ProcessIdentity: "s", ProcessGroupID: 12, GroupIdentity: "g"},
		},
		{
			name:    "no supervisor pid",
			message: supervisorMessage{Type: "ready", SupervisorPID: 0, ProcessIdentity: "s", ProcessGroupID: 12, GroupIdentity: "g"},
		},
		{
			name:    "no process group",
			message: supervisorMessage{Type: "ready", SupervisorPID: 11, ProcessIdentity: "s", ProcessGroupID: 0, GroupIdentity: "g"},
		},
		{
			name:    "no process identity",
			message: supervisorMessage{Type: "ready", SupervisorPID: 11, ProcessIdentity: "", ProcessGroupID: 12, GroupIdentity: "g"},
		},
		{
			name:    "no group identity",
			message: supervisorMessage{Type: "ready", SupervisorPID: 11, ProcessIdentity: "s", ProcessGroupID: 12, GroupIdentity: ""},
		},
	}
	for _, testCase := range invalid {
		t.Run(testCase.name, func(t *testing.T) {
			process := newTestSupervisorProcess()
			process.messages <- testCase.message
			if err := process.awaitReady(context.Background()); err == nil {
				t.Fatalf("awaitReady accepted %+v", testCase.message)
			}
		})
	}

	t.Run("decode failure", func(t *testing.T) {
		process := newTestSupervisorProcess()
		if _, err := process.stderr.Write([]byte("supervisor stderr")); err != nil {
			t.Fatal(err)
		}
		process.decodeErrors <- io.ErrUnexpectedEOF
		err := process.awaitReady(context.Background())
		if err == nil || !strings.Contains(err.Error(), "supervisor stderr") {
			t.Fatalf("decode failure error = %v", err)
		}
	})

	t.Run("early exit", func(t *testing.T) {
		process := newTestSupervisorProcess()
		process.wait <- errors.New("exit status 2")
		err := process.awaitReady(context.Background())
		if err == nil || !strings.Contains(err.Error(), "exited before readiness") {
			t.Fatalf("early exit error = %v", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		process := newTestSupervisorProcess()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := process.awaitReady(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled context error = %v", err)
		}
	})
}

func newTestSupervisorProcess() *supervisorProcess {
	return &supervisorProcess{
		messages:     make(chan supervisorMessage, 1),
		decodeErrors: make(chan error, 1),
		wait:         make(chan error, 1),
		stderr:       newLimitBuffer(maxSupervisorErrorBytes),
	}
}

func decodeSupervisorMessages(t *testing.T, body []byte) []supervisorMessage {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(body))
	var messages []supervisorMessage
	for {
		var message supervisorMessage
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			return messages
		}
		if err != nil {
			t.Fatalf("decode supervisor messages: %v", err)
		}
		messages = append(messages, message)
	}
}

func writeTestFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func containsEnvironment(environment []string, entry string) bool {
	for _, value := range environment {
		if value == entry {
			return true
		}
	}
	return false
}

func countEnvironment(environment []string, prefix string) int {
	count := 0
	for _, value := range environment {
		if strings.HasPrefix(value, prefix) {
			count++
		}
	}
	return count
}

func identityValues(environment []string) []string {
	var values []string
	for _, value := range environment {
		if strings.HasPrefix(value, "FACTORY_RUN_ID=") || strings.HasPrefix(value, "FACTORY_SESSION_ID=") {
			values = append(values, value)
		}
	}
	return values
}

func itoa(value int64) string {
	return strconv.FormatInt(value, 10)
}

func TestRuntimeEnvironmentPutsTheFactoryBinaryOnPath(t *testing.T) {
	// Gap 6. AgentUpdatePromptContract tells the agent to run "factory update",
	// a bare command. The worker copied its own PATH to the child and added
	// nothing, so on a host where the binary lives in ~/.factory/bin and that
	// directory is not on PATH, every agent got "command not found: factory".
	// All three agent_update attempts on the live host hit exactly this; one
	// escaped only by running find until it located the binary.
	dir := t.TempDir()
	binary := filepath.Join(dir, "factory")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	environment := runtimeEnvironmentWithExecutable(
		"run", "session", "attempt", "/tmp/sock", "token", binary,
	)

	var path string
	for _, value := range environment {
		if key, rest, _ := strings.Cut(value, "="); key == "PATH" {
			path = rest
		}
	}
	if path == "" {
		t.Fatal("PATH is absent from the agent environment")
	}
	if first, _, _ := strings.Cut(path, string(os.PathListSeparator)); first != dir {
		t.Fatalf("PATH does not begin with the factory binary's directory:\nfirst = %q\nwant  = %q\nPATH  = %s", first, dir, path)
	}
}

func TestRuntimeEnvironmentLeavesPathAloneWithoutAnExecutable(t *testing.T) {
	// If we cannot determine our own path, do not invent one and do not drop
	// the inherited PATH: the agent still needs git, gh, and its own runtime.
	environment := runtimeEnvironmentWithExecutable(
		"run", "session", "attempt", "/tmp/sock", "token", "",
	)
	var found bool
	for _, value := range environment {
		if key, rest, _ := strings.Cut(value, "="); key == "PATH" {
			found = true
			if rest != os.Getenv("PATH") {
				t.Fatalf("PATH was modified without an executable: %q", rest)
			}
		}
	}
	if !found && os.Getenv("PATH") != "" {
		t.Fatal("inherited PATH was dropped")
	}
}
