package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"testing"
	"time"

	"github.com/chenhg5/cc-connect/core"
)

func TestCodexAutoRetryClassification(t *testing.T) {
	for _, tc := range []struct {
		text     string
		eligible bool
		delay    time.Duration
	}{
		{"rate limit exceeded: Your requests to gpt-6.1-sol in eastus2 have exceeded token rate limit", true, 0},
		{"rate_limit_exceeded; Retry-After: 75", true, 75 * time.Second},
		{"rate limit exceeded; try again in 2 minutes", true, 2 * time.Minute},
		{`rate_limit_error; "retry_after": 1250ms`, true, 1250 * time.Millisecond},
		{"429 insufficient_quota rate limit", false, 0}, {"429 billing limit", false, 0},
		{"rate limit: insufficient balance", false, 0}, {"401 unauthorized", false, 0},
		{"invalid_encrypted_content", false, 0}, {"model_not_found", false, 0}, {"429", false, 0},
		{"connection reset", false, 0},
	} {
		d, ok := codexRateLimitRetryDelay(errors.New(tc.text))
		if ok != tc.eligible || d != tc.delay {
			t.Errorf("%q: %v %v", tc.text, d, ok)
		}
	}
	if _, ok := codexRateLimitRetryDelay(nil); ok {
		t.Fatal("nil error eligible")
	}
}

func TestCodexAutoRetryHTTPDateMinimum(t *testing.T) {
	reset := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	delay, ok := codexRateLimitRetryDelay(errors.New("rate limit; Retry-After: " + reset.Format(http.TimeFormat)))
	if !ok || delay < 58*time.Second || delay > time.Minute {
		t.Fatalf("HTTP-date delay = %v, %v", delay, ok)
	}
}

func TestCodexAutoRetryBufferedAssistantProgress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &codexSession{ctx: ctx, events: make(chan core.Event, 8)}
	got := ""
	s.SetRetryProgressHandler(func(text string) { got = text })
	s.handleItemCompleted(map[string]any{"item": map[string]any{"type": "agent_message", "text": "real output"}})
	if got != "real output" {
		t.Fatalf("buffered output did not reset: %q", got)
	}
	if len(s.events) != 0 {
		t.Fatal("progress changed rendering events")
	}
	s.SetRetryProgressHandler(nil)
	s.handleItemCompleted(map[string]any{"item": map[string]any{"type": "agent_message", "text": "later"}})
	if got != "real output" {
		t.Fatal("removed callback still invoked")
	}
}

func TestCodexAutoRetryWaitsForFailedProcess(t *testing.T) {
	s := &codexSession{cmds: map[*exec.Cmd]struct{}{new(exec.Cmd): {}}}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := s.PrepareRetry(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("did not wait for old producer: %v", err)
	}
	s.cmds = nil
	if err := s.PrepareRetry(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAppServerAutoRetryIgnoresOldTerminalEvent(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "new-turn"}
	s.handleNotification("turn/completed", json.RawMessage(`{"turn":{"id":"old-turn","status":"failed","error":{"message":"rate limit exceeded"}}}`))
	if len(s.events) != 0 || s.currentTurn != "new-turn" {
		t.Fatal("old failure terminated new retry")
	}
	s.handleNotification("error", json.RawMessage(`{"turnId":"old-turn","error":{"message":"rate limit exceeded"}}`))
	if len(s.events) != 0 || s.currentTurn != "new-turn" {
		t.Fatal("late error terminated new retry")
	}
	s.handleNotification("error", json.RawMessage(`{"willRetry":true,"error":{"message":"rate limit exceeded"}}`))
	if len(s.events) != 0 {
		t.Fatal("Codex native retry became terminal")
	}
	if err := s.PrepareRetry(context.Background()); err == nil {
		t.Fatal("active turn should prevent automatic resend")
	}
}

func TestAppServerAutoRetryReportsBufferedOutput(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8)}
	got := ""
	s.SetRetryProgressHandler(func(text string) { got = text })
	s.handleItemCompleted(map[string]any{"type": "agentMessage", "text": "implemented a change"})
	if got != "implemented a change" || len(s.pendingMsgs) != 1 || len(s.events) != 0 {
		t.Fatal("buffered output progress changed rendering")
	}
}

func TestAppServerAutoRetryLateStartReplyCannotResurrectFailedTurn(t *testing.T) {
	s := &appServerSession{events: make(chan core.Event, 8), currentTurn: "failed-turn"}
	s.failTurn(errors.New("rate limit exceeded"))
	s.recordStartedTurn("failed-turn")
	if s.currentTurn != "" {
		t.Fatal("delayed RPC reply resurrected completed failure")
	}
	if err := s.PrepareRetry(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.recordStartedTurn("retry-turn")
	if s.currentTurn != "retry-turn" {
		t.Fatal("new turn was not started")
	}
}
