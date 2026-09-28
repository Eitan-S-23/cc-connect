package claudecode

import (
	"context"
	"testing"

	"github.com/chenhg5/cc-connect/core"
)

func TestHandleResultFailureNeverEmitsSuccessfulCompletion(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"http", map[string]any{"is_error": true, "result": "HTTP 503"}, "HTTP 503"},
		{"no_status", map[string]any{"is_error": true, "errors": []any{"TLS handshake failed", "connection reset"}}, "TLS handshake failed; connection reset"},
		{"turn_limit", map[string]any{"subtype": "error_max_turns"}, "error_max_turns"},
		{"budget_limit", map[string]any{"subtype": "error_max_budget_usd"}, "error_max_budget_usd"},
		{"future_error", map[string]any{"subtype": "error_future_provider_reason"}, "error_future_provider_reason"},
		{"no_details", map[string]any{"is_error": true}, "turn failed (no details)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cs := &claudeSession{ctx: context.Background(), events: make(chan core.Event, 2)}
			tc.raw["session_id"] = "failed-session"
			cs.handleResult(tc.raw)
			event := <-cs.events
			if event.Type != core.EventError || !event.Done || event.Error == nil || event.Error.Error() != tc.want {
				t.Fatalf("failed result event = %+v, want terminal error %q", event, tc.want)
			}
			if event.SessionID != "failed-session" || len(cs.events) != 0 {
				t.Fatal("failure lost its session ID or emitted duplicate terminal events")
			}
		})
	}
}
