package codex

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestAppServerAttentionRetryIsNotTerminal(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 4), currentTurn: "turn-1"}
	s.handleNotification("error", json.RawMessage(`{"error":{"message":"HTTP 529"},"willRetry":true}`))
	if len(s.events) != 0 || s.currentTurn != "turn-1" {
		t.Fatal("a retry notification prematurely ended the turn")
	}
	s.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"turn-1","status":"completed"}}`))
	if event := <-s.events; event.Type != core.EventResult || !event.Done {
		t.Fatalf("recovered turn did not complete normally: %+v", event)
	}
}

func TestAppServerAttentionTerminalErrorsAreDeduplicated(t *testing.T) {
	for _, payload := range []string{
		`{"error":{"message":"connection reset"},"willRetry":false}`,
		`{"message":"quota exhausted"}`,
		`{"error":{"message":"HTTP 503"}}`,
		`{"error":{},"willRetry":false}`,
	} {
		t.Run(payload, func(t *testing.T) {
			s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "turn-1"}
			s.handleNotification("error", json.RawMessage(payload))
			s.handleNotification("error", json.RawMessage(payload))
			s.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"turn-1","status":"failed","error":{"message":"failed"}}}`))
			if len(s.events) != 1 {
				t.Fatalf("terminal events = %d, want one", len(s.events))
			}
			if event := <-s.events; event.Type != core.EventError || event.Error == nil {
				t.Fatalf("terminal error was lost: %+v", event)
			}
		})
	}
}

func TestAppServerAttentionInterruptedIsNotSuccess(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 2), currentTurn: "turn-1"}
	s.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"turn-1","status":"interrupted"}}`))
	if event := <-s.events; event.Type != core.EventError || !errors.Is(event.Error, context.Canceled) {
		t.Fatalf("interrupted turn reported as successful: %+v", event)
	}
}
