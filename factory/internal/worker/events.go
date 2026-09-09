package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/owainlewis/factory/internal/protocol"
)

type eventSender struct {
	client    *client
	attemptID string
	token     string
	kind      string
	input     chan protocol.AttemptEvent
	done      chan struct{}
	mutex     sync.Mutex
	next      int64
	disabled  bool
	closed    bool
}

func newEventSender(ctx context.Context, client *client, attemptID, token, kind string) *eventSender {
	sender := &eventSender{
		client: client, attemptID: attemptID, token: token, kind: kind,
		input: make(chan protocol.AttemptEvent, 100), done: make(chan struct{}),
	}
	go sender.run(ctx)
	return sender
}

func (sender *eventSender) enqueue(stream, text string, truncated bool) {
	sender.mutex.Lock()
	if sender.disabled || sender.closed {
		sender.mutex.Unlock()
		return
	}
	payload := eventPayload(stream, text, truncated)
	event := protocol.AttemptEvent{Sequence: sender.next, Kind: sender.kind, Payload: payload}
	select {
	case sender.input <- event:
		sender.next++
	default:
		sender.disabled = true
	}
	sender.mutex.Unlock()
}

func (sender *eventSender) closeAndWait(timeout time.Duration) {
	sender.mutex.Lock()
	if !sender.closed {
		close(sender.input)
		sender.closed = true
	}
	sender.mutex.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-sender.done:
	case <-timer.C:
	}
}

func (sender *eventSender) run(ctx context.Context) {
	defer close(sender.done)
	for event := range sender.input {
		backoff := 100 * time.Millisecond
		for {
			requestContext, cancel := context.WithTimeout(ctx, requestTimeout)
			err := sender.client.events(requestContext, sender.attemptID, protocol.EventBatchRequest{
				LeaseToken: sender.token,
				Events:     []protocol.AttemptEvent{event},
			})
			cancel()
			if err == nil {
				break
			}
			var apiError *APIError
			if errors.As(err, &apiError) && apiError.Status < 500 {
				sender.mutex.Lock()
				sender.disabled = true
				sender.mutex.Unlock()
				return
			}
			timer := time.NewTimer(backoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			backoff *= 2
			if backoff > 2*time.Second {
				backoff = 2 * time.Second
			}
		}
	}
}

func eventPayload(stream, text string, truncated bool) json.RawMessage {
	if stream == "stdout" && !truncated && json.Valid([]byte(text)) && len(text) <= protocol.MaxEventBytes {
		return json.RawMessage(append([]byte(nil), text...))
	}
	maximumText := protocol.MaxEventBytes - 256
	if len(text) < maximumText {
		maximumText = len(text)
	}
	encode := func(length int, wasTruncated bool) json.RawMessage {
		payload, _ := json.Marshal(map[string]any{
			"stream":    stream,
			"text":      boundedText(text, length),
			"truncated": wasTruncated,
		})
		return payload
	}
	payload := encode(maximumText, truncated || maximumText < len(text))
	if len(payload) <= protocol.MaxEventBytes {
		return payload
	}
	low, high := 0, maximumText
	for low < high {
		middle := low + (high-low+1)/2
		if len(encode(middle, true)) <= protocol.MaxEventBytes {
			low = middle
		} else {
			high = middle - 1
		}
	}
	return encode(low, true)
}
