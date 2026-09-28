package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type attentionControlledSession struct {
	events    chan Event
	sent      chan string
	sendError error
	closed    atomic.Bool
	closeOnce sync.Once
}

func newAttentionControlledSession() *attentionControlledSession {
	return &attentionControlledSession{events: make(chan Event, 16), sent: make(chan string, 8)}
}

func (s *attentionControlledSession) Send(_ string, id string, _ []ImageAttachment, _ []FileAttachment) error {
	s.sent <- id
	return s.sendError
}
func (s *attentionControlledSession) RespondPermission(string, PermissionResult) error { return nil }
func (s *attentionControlledSession) Events() <-chan Event                             { return s.events }
func (s *attentionControlledSession) CurrentSessionID() string                         { return "attention-controlled" }
func (s *attentionControlledSession) Alive() bool                                      { return !s.closed.Load() }
func (s *attentionControlledSession) Close() error {
	s.closeOnce.Do(func() { s.closed.Store(true); close(s.events) })
	return nil
}

type attentionControlledAgent struct {
	cujAgent
	session *attentionControlledSession
}

func (a *attentionControlledAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}

func waitAttentionSend(t *testing.T, s *attentionControlledSession, want string) {
	t.Helper()
	select {
	case got := <-s.sent:
		if got != want {
			t.Fatalf("sent message = %q, want %q", got, want)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("agent never received message %q", want)
	}
}

func newFailureAttentionEngine(t *testing.T, agent Agent) (*Engine, *attentionRecordingPlatform) {
	t.Helper()
	p := &attentionRecordingPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	e := NewEngine("attention", agent, []Platform{p}, t.TempDir()+"/sessions.json", LangEnglish)
	e.SetAttentionNotify(AttentionNotifyCfg{
		Enabled: true, OnTurnComplete: true, OnBlocked: true, OnError: true, MentionUser: true,
	})
	t.Cleanup(func() { _ = e.Stop() })
	return e, p
}

func sendAttentionMessage(e *Engine, p Platform, id, user string) {
	e.ReceiveMessage(p, &Message{
		SessionKey: "test:attention", Platform: "test", MessageID: id,
		UserID: user, UserName: user, Content: "do the work", ReplyCtx: id,
	})
}

func waitAttentionIdle(t *testing.T, e *Engine) {
	t.Helper()
	waitAttentionSessionIdle(t, e, "test:attention")
}

func waitAttentionSessionIdle(t *testing.T, e *Engine, sessionKey string) {
	t.Helper()
	s := e.sessions.GetOrCreateActive(sessionKey)
	deadline := time.Now().Add(3 * time.Second)
	for s.Busy() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if s.Busy() {
		t.Fatal("agent turn did not release the session")
	}
}

func TestAttentionNotifyFailureKinds(t *testing.T) {
	for _, detail := range []string{
		"400", "401", "403", "402 quota exhausted", "404 model not found", "408",
		"413 context too large", "422", "429", "500", "502", "503", "529",
		"connection reset by peer", "TLS certificate error", "unexpected EOF",
		"invalid response JSON", "provider-specific failure without a status code", "",
	} {
		t.Run(detail, func(t *testing.T) {
			var err error
			if detail != "" {
				err = errors.New(detail)
			}
			e, p := newFailureAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventError, Error: err}}})
			e.attentionNotify.MinDuration = time.Hour
			sendAttentionMessage(e, p, "failure", "ou_alice")
			waitAttentionIdle(t, e)
			got := p.getNotifications()
			if len(got) != 1 || got[0].userID != "ou_alice" || got[0].text != e.i18n.T(MsgAttentionTurnFailed) {
				t.Fatalf("terminal error %q: notifications = %+v, want one error mention", detail, got)
			}
		})
	}
}

func TestAttentionNotifyFailedResultIsNotCompletion(t *testing.T) {
	e, p := newFailureAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{
		Type: EventResult, Content: "not successful", Done: true, Error: errors.New("arbitrary failure"),
	}}})
	sendAttentionMessage(e, p, "failed-result", "ou_alice")
	waitAttentionIdle(t, e)
	got := p.getNotifications()
	if len(got) != 1 || got[0].text != e.i18n.T(MsgAttentionTurnFailed) {
		t.Fatalf("failed result reported as completion: %+v", got)
	}
}

func TestAttentionNotifyStartFailure(t *testing.T) {
	e, p := newFailureAttentionEngine(t, &cujAgent{failStartCount: 10, failStartErr: errors.New("executable missing")})
	sendAttentionMessage(e, p, "start-failure", "ou_alice")
	waitAttentionIdle(t, e)
	got := p.getNotifications()
	if len(got) != 1 || got[0].text != e.i18n.T(MsgAttentionStartFailed) {
		t.Fatalf("start failure notifications = %+v", got)
	}
}

func TestAttentionNotifyRecoverableToolFailureIsNotTerminal(t *testing.T) {
	failed := false
	e, p := newFailureAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{
		{Type: EventToolResult, ToolName: "Bash", ToolStatus: "failed", ToolSuccess: &failed, ToolResult: "command failed"},
		{Type: EventResult, Done: false, Content: "compacting"},
		{Type: EventResult, Done: true, Content: "recovered and finished"},
	}})
	sendAttentionMessage(e, p, "recovered", "ou_alice")
	waitAttentionIdle(t, e)
	got := p.getNotifications()
	if len(got) != 1 || got[0].text != e.i18n.T(MsgAttentionTurnComplete) {
		t.Fatalf("recoverable tool failure triggered a terminal alert: %+v", got)
	}
}

func TestAttentionNotifyFailureCanBeDisabled(t *testing.T) {
	e, p := newFailureAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventError, Error: errors.New("failure")}}})
	e.attentionNotify.OnError = false
	sendAttentionMessage(e, p, "no-alert", "ou_alice")
	waitAttentionIdle(t, e)
	if got := p.getNotifications(); len(got) != 0 {
		t.Fatalf("on_error=false sent alerts: %+v", got)
	}
}

func TestAttentionNotifyDoesNotRepeatRawErrors(t *testing.T) {
	err := fmt.Errorf("provider failed: Authorization: Bearer test-secret-value")
	e, p := newFailureAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventError, Error: err}}})
	sendAttentionMessage(e, p, "redacted-alert", "ou_alice")
	waitAttentionIdle(t, e)
	got := p.getNotifications()
	if len(got) != 1 || strings.Contains(got[0].text, "test-secret-value") {
		t.Fatalf("expected a categorical alert without raw error details: %+v", got)
	}
}

func TestAttentionNotifyRuntimeStops(t *testing.T) {
	for _, kind := range []string{"send", "idle", "deadline", "exit", "error_then_exit", "stop", "permission_stop", "permission_deadline", "permission_error", "permission_exit", "recall"} {
		t.Run(kind, func(t *testing.T) {
			s := newAttentionControlledSession()
			if kind == "send" {
				s.sendError = errors.New("broken pipe")
			}
			e, p := newFailureAttentionEngine(t, &attentionControlledAgent{session: s})
			switch kind {
			case "idle":
				e.eventIdleTimeout = 50 * time.Millisecond
			case "deadline", "permission_deadline":
				e.maxTurnTime = 100 * time.Millisecond
			}
			sendAttentionMessage(e, p, "runtime", "ou_alice")
			waitAttentionSend(t, s, "runtime")
			want := MsgAttentionTurnFailed
			count := 1
			switch kind {
			case "send":
				want = MsgAttentionSendFailed
			case "idle":
				want = MsgAttentionIdleTimeout
			case "deadline":
				want = MsgAttentionTurnTimeout
				waitForAttention(t, p, 1)
				s.events <- Event{Type: EventResult, Done: true}
			case "exit":
				want = MsgAttentionProcessExited
				_ = s.Close()
			case "error_then_exit":
				s.events <- Event{Type: EventError, Error: errors.New("unexpected failure")}
				s.events <- Event{Type: EventError, Error: errors.New("duplicate failure")}
				_ = s.Close()
			case "permission_stop", "permission_deadline", "permission_error", "permission_exit":
				s.events <- Event{Type: EventPermissionRequest, RequestID: "req-1", ToolName: "Bash"}
				waitForAttention(t, p, 1)
				count = 2
				switch kind {
				case "permission_stop":
					want = MsgAttentionStopped
					e.stopInteractiveSession("test:attention", p, "runtime")
				case "permission_deadline":
					want = MsgAttentionTurnTimeout
				case "permission_error":
					s.events <- Event{Type: EventText, Content: "partial progress"}
					s.events <- Event{Type: EventPermissionRequest, RequestID: "stale-request", ToolName: "Read"}
					s.events <- Event{Type: EventError, Error: errors.New("TLS connection closed while waiting")}
				case "permission_exit":
					want = MsgAttentionProcessExited
					_ = s.Close()
				}
			case "stop":
				want = MsgAttentionStopped
				e.stopInteractiveSession("test:attention", p, "runtime")
			case "recall":
				count = 0
				e.stopInteractiveSessionSilently("test:attention")
			}
			waitAttentionIdle(t, e)
			got := p.getNotifications()
			if len(got) != count {
				t.Fatalf("notifications = %+v, want %d", got, count)
			}
			if count > 0 && (got[count-1].text != e.i18n.T(want) || got[count-1].userID != "ou_alice") {
				t.Fatalf("terminal alert = %+v, want %s for original sender", got[count-1], want)
			}
		})
	}
}

func TestAttentionNotifyOneTerminalAndOneAlertPerRequest(t *testing.T) {
	e, p := newFailureAttentionEngine(t, &cujAgent{})
	state := &interactiveState{platform: p, currentUserID: "ou_alice"}
	n := e.newAttentionTurn(state, "original-message", time.Now())
	n.notifyBlocked("req-1", "permission needed")
	n.notifyBlocked("req-1", "duplicate permission")
	n.notifyBlocked("req-2", "another question")
	n.finish(false, false)
	n.finish(true, false)
	n.finish(false, false)
	if got := p.getNotifications(); len(got) != 3 {
		t.Fatalf("duplicate request/terminal alerts: %+v", got)
	}
}

func TestAttentionNotifyFailureDoesNotBreakAgentTurn(t *testing.T) {
	e, p := newFailureAttentionEngine(t, &cujAgent{})
	p.notifyError = errors.New("notification transport unavailable")
	sendAttentionMessage(e, p, "notice-failed", "ou_alice")
	waitAttentionIdle(t, e)
	if got := p.getNotifications(); len(got) != 1 {
		t.Fatalf("notification attempts = %d, want one", len(got))
	}
	if !strings.Contains(strings.Join(p.getSent(), "\n"), "ok") {
		t.Fatal("notification failure prevented the original final reply")
	}
}

func TestAttentionNotifySilentSuccessAndShutdownStaySilent(t *testing.T) {
	e, p := newFailureAttentionEngine(t, &cujAgent{nextSessionEvents: []Event{{Type: EventResult, Done: true, Content: "NO_REPLY"}}})
	sendAttentionMessage(e, p, "silent", "ou_alice")
	waitAttentionIdle(t, e)
	e.cancel()
	e.notifyAttention(p, "ctx", "ou_alice", "Alice", "must not send during shutdown")
	if got := p.getNotifications(); len(got) != 0 {
		t.Fatalf("silent path emitted alerts: %+v", got)
	}
}

type attentionBrokenReplyPlatform struct{ *attentionRecordingPlatform }

func (p *attentionBrokenReplyPlatform) Send(context.Context, any, string) error {
	return errors.New("final reply could not be delivered")
}

func TestAttentionNotifyFinalDeliveryFailureIsNotSuccess(t *testing.T) {
	e, recorded := newFailureAttentionEngine(t, &cujAgent{})
	p := &attentionBrokenReplyPlatform{recorded}
	sendAttentionMessage(e, p, "delivery-failure", "ou_alice")
	waitAttentionIdle(t, e)
	got := recorded.getNotifications()
	if len(got) != 1 || got[0].text != e.i18n.T(MsgAttentionDeliveryFailed) {
		t.Fatalf("delivery failure reported as success: %+v", got)
	}
}

func TestAttentionNotifyBackgroundLifecycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []Event
		want   MsgKey
	}{
		{"idle_exit", nil, ""},
		{"compaction_only", []Event{{Type: EventResult, Done: false}}, ""},
		{"complete", []Event{{Type: EventResult, Done: true, Content: "done"}}, MsgAttentionTurnComplete},
		{"silent", []Event{{Type: EventResult, Done: true, Content: "NO_REPLY"}}, ""},
		{"mid_turn_exit", []Event{{Type: EventText, Content: "working"}}, MsgAttentionProcessExited},
		{"failed", []Event{{Type: EventError, Error: errors.New("network error")}}, MsgAttentionTurnFailed},
		{"failed_result", []Event{{Type: EventResult, Done: true, Error: errors.New("failed")}}, MsgAttentionTurnFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttentionControlledSession()
			e, p := newFailureAttentionEngine(t, &attentionControlledAgent{session: s})
			state := &interactiveState{platform: p, replyCtx: "background-parent", currentUserID: "ou_alice", agentSession: s}
			session := e.sessions.GetOrCreateActive("test:attention")
			for _, event := range tc.events {
				s.events <- event
			}
			_ = s.Close()
			ctx, cancel := context.WithCancel(e.ctx)
			done := make(chan struct{})
			e.runUnsolicitedReader(ctx, cancel, done, state, s, session, e.sessions, "test:attention", "")
			got := p.getNotifications()
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("idle/silent activity generated an alert: %+v", got)
				}
			} else if len(got) != 1 || got[0].text != e.i18n.T(tc.want) || got[0].userID != "ou_alice" {
				t.Fatalf("background notifications = %+v, want %s", got, tc.want)
			}
		})
	}
}

func TestAttentionNotifySendFailureDrainsLateErrorOnRecovery(t *testing.T) {
	s := newAttentionControlledSession()
	e, p := newFailureAttentionEngine(t, &attentionControlledAgent{session: s})
	sendAttentionMessage(e, p, "first", "ou_alice")
	waitAttentionSend(t, s, "first")
	s.events <- Event{Type: EventResult, Done: true, Content: "done"}
	waitAttentionIdle(t, e)

	s.sendError = errors.New("send failed")
	sendAttentionMessage(e, p, "failed", "ou_bob")
	waitAttentionSend(t, s, "failed")
	waitAttentionIdle(t, e)
	e.interactiveMu.Lock()
	state := e.interactiveStates["test:attention"]
	e.interactiveMu.Unlock()
	state.mu.Lock()
	resync := state.eventsNeedResync
	state.mu.Unlock()
	if !resync {
		t.Fatal("failed Send left the event stream clean; a late error could trigger a duplicate alert")
	}
	s.events <- Event{Type: EventError, Error: errors.New("late duplicate error from the failed send")}

	s.sendError = nil
	sendAttentionMessage(e, p, "recovered", "ou_carol")
	waitAttentionSend(t, s, "recovered")
	s.events <- Event{Type: EventResult, Done: true, Content: "recovered"}
	waitAttentionIdle(t, e)
	got := p.getNotifications()
	if len(got) != 3 || got[1].text != e.i18n.T(MsgAttentionSendFailed) || got[2].userID != "ou_carol" {
		t.Fatalf("late error was not isolated from the recovered turn: %+v", got)
	}
}
