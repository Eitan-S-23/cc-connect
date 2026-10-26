package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type retryTestSession struct {
	*attentionControlledSession
	mu        sync.Mutex
	progress  func(string)
	prompts   chan string
	id        string
	minimum   time.Duration
	prepare   func()
	failSends int
}

func (s *retryTestSession) Send(prompt, _ string, images []ImageAttachment, files []FileAttachment) error {
	if len(images)+len(files) != 0 {
		return errors.New("unexpected replayed attachments")
	}
	s.prompts <- prompt
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failSends > 0 {
		s.failSends--
		return errors.New("rate limit exceeded")
	}
	return nil
}
func (s *retryTestSession) CurrentSessionID() string { s.mu.Lock(); defer s.mu.Unlock(); return s.id }
func (s *retryTestSession) RateLimitRetryDelay(err error) (time.Duration, bool) {
	return s.minimum, err != nil && strings.Contains(err.Error(), "rate limit")
}
func (s *retryTestSession) PrepareRetry(ctx context.Context) error {
	if s.prepare != nil {
		s.prepare()
	}
	return ctx.Err()
}
func (s *retryTestSession) SetRetryProgressHandler(f func(string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.progress = f
}

type retryTestAgent struct {
	cujAgent
	session *retryTestSession
}

func (a *retryTestAgent) StartSession(context.Context, string) (AgentSession, error) {
	return a.session, nil
}

func newAutoRetryEngine(t *testing.T) (*Engine, *attentionRecordingPlatform, *retryTestSession) {
	t.Helper()
	s := &retryTestSession{attentionControlledSession: newAttentionControlledSession(), prompts: make(chan string, 16), id: "original-thread"}
	e, p := newFailureAttentionEngine(t, &retryTestAgent{session: s})
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "state.json")))
	e.SetAdminFrom("admin")
	e.autoRetryCommandCard([]string{"on"}, "test:attention", "admin", false)
	return e, p, s
}

func awaitAutoRetry(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if f() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for autoretry state")
}
func autoRetryRunForTest(t *testing.T, e *Engine) *autoRetryRun {
	t.Helper()
	var r *autoRetryRun
	awaitAutoRetry(t, func() bool {
		e.autoRetryMu.Lock()
		defer e.autoRetryMu.Unlock()
		r = e.autoRetryRuns["test:attention"]
		return r != nil
	})
	return r
}
func retryPrompt(t *testing.T, s *retryTestSession) string {
	t.Helper()
	select {
	case p := <-s.prompts:
		return p
	case <-time.After(3 * time.Second):
		t.Fatal("missing prompt")
		return ""
	}
}
func startAutoRetryTest(t *testing.T, e *Engine, p Platform, s *retryTestSession) *autoRetryRun {
	t.Helper()
	sendAttentionMessage(e, p, "request", "admin")
	if p := retryPrompt(t, s); p != "do the work" {
		t.Fatalf("initial prompt: %q", p)
	}
	r := autoRetryRunForTest(t, e)
	r.mu.Lock()
	r.delays = [3]time.Duration{}
	r.mu.Unlock()
	return r
}

func TestCUJ_AR1_ConsecutiveFailuresAndNewTask(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	startAutoRetryTest(t, e, p, s)
	for i := 0; i < 3; i++ {
		s.events <- Event{Type: EventError, Error: errors.New("token rate limit exceeded")}
		if got := retryPrompt(t, s); got != e.i18n.T(MsgAutoRetryPrompt) {
			t.Fatalf("wrong continuation: %q", got)
		}
	}
	s.events <- Event{Type: EventError, Error: errors.New("token rate limit exceeded")}
	waitAttentionIdle(t, e)
	if len(s.prompts) != 0 {
		t.Fatal("retry budget exceeded")
	}
	if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgAutoRetryExhausted)) {
		t.Fatal("missing exhausted receipt")
	}
	if got := e.sessions.GetOrCreateActive("test:attention").GetAgentSessionID(); got != "original-thread" {
		t.Fatalf("thread changed: %q", got)
	}
	e.ReceiveMessage(p, &Message{SessionKey: "test:attention", UserID: "admin", Platform: "test", Content: "/autoretry"})
	sendAttentionMessage(e, p, "new-task", "admin")
	retryPrompt(t, s)
	r := autoRetryRunForTest(t, e)
	r.mu.Lock()
	attempts := r.attempts
	r.mu.Unlock()
	if attempts != 0 {
		t.Fatal("new task inherited retry count")
	}
	s.events <- Event{Type: EventResult, Done: true, Content: "done"}
	waitAttentionIdle(t, e)
}

func TestAutoRetryProgressResetsBeforeCompletion(t *testing.T) {
	for _, kind := range []string{"text", "tool", "buffered"} {
		t.Run(kind, func(t *testing.T) {
			e, p, s := newAutoRetryEngine(t)
			r := startAutoRetryTest(t, e, p, s)
			for i := 0; i < 2; i++ {
				s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
				retryPrompt(t, s)
			}
			switch kind {
			case "text":
				s.events <- Event{Type: EventText, Content: "implemented the first change"}
			case "tool":
				s.events <- Event{Type: EventToolResult, ToolStatus: "completed"}
			case "buffered":
				s.mu.Lock()
				s.progress("verified a change")
				s.mu.Unlock()
			}
			awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.attempts == 0 })
			s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
			retryPrompt(t, s)
			r.mu.Lock()
			attempts := r.attempts
			r.mu.Unlock()
			if attempts != 1 {
				t.Fatalf("attempts = %d", attempts)
			}
			s.events <- Event{Type: EventResult, Done: true, Content: "done"}
			waitAttentionIdle(t, e)
		})
	}
}

func TestAutoRetryIgnoresNonProgress(t *testing.T) {
	zero, one := 0, 1
	yes, no := true, false
	for _, event := range []Event{
		{Type: EventThinking, Content: "Reconnecting"}, {Type: EventText, Content: "NO_REPLY"},
		{Type: EventText, Content: "retrying", Synthetic: true}, {Type: EventError, Error: errors.New("rate limit")},
		{Type: EventToolUse, ToolName: "Bash"}, {Type: EventToolResult, ToolResult: "unknown"},
		{Type: EventToolResult, ToolStatus: "failed", ToolExitCode: &zero}, {Type: EventToolResult, ToolSuccess: &no},
		{Type: EventToolResult, ToolSuccess: &yes, ToolExitCode: &one}, {Type: EventResult, Done: false, Content: "compacting"},
	} {
		r := &autoRetryRun{attempts: 2}
		r.observe(event)
		if r.attempts != 2 {
			t.Fatalf("false progress: %+v", event)
		}
	}
}

func TestCUJ_AR2_OffCancelsWaitingWithoutInterruptingWork(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	r := startAutoRetryTest(t, e, p, s)
	r.mu.Lock()
	r.delays = [3]time.Duration{time.Hour, time.Hour, time.Hour}
	r.mu.Unlock()
	s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
	awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return !r.next.IsZero() })
	card := e.handleAuthenticatedCardNav("nav:/autoretry", "test:attention", "admin")
	if !strings.Contains(card.RenderText(), "0/3") {
		t.Fatal("card missing retry status")
	}
	e.handleAuthenticatedCardNav("act:/autoretry off", "test:attention", "admin")
	waitAttentionIdle(t, e)
	if len(s.prompts) != 0 || !s.Alive() {
		t.Fatal("off sent a retry or killed the agent")
	}
	sendAttentionMessage(e, p, "manual", "admin")
	retryPrompt(t, s)
	s.events <- Event{Type: EventResult, Done: true, Content: "done"}
	waitAttentionIdle(t, e)
}

func TestAutoRetryNewInputCancelsWaitAndDrainsQueue(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	r := startAutoRetryTest(t, e, p, s)
	r.mu.Lock()
	r.delays[0] = time.Hour
	r.mu.Unlock()
	s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
	awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return !r.next.IsZero() })
	e.ReceiveMessage(p, &Message{SessionKey: "test:attention", Platform: "test", UserID: "admin", Content: "different task", ReplyCtx: "new"})
	if got := retryPrompt(t, s); got != "different task" {
		t.Fatalf("stale retry displaced user: %q", got)
	}
	s.events <- Event{Type: EventResult, Done: true, Content: "done"}
	waitAttentionIdle(t, e)
}

func TestAutoRetryCardAuthorizationPersistenceAndIsolation(t *testing.T) {
	a, b := newNotifyTestEngine(t, "a"), newNotifyTestEngine(t, "b")
	if a.autoRetryEnabled() {
		t.Fatal("must default off")
	}
	a.handleAuthenticatedCardNav("act:/autoretry on", "test:admin", "guest")
	if a.autoRetryEnabled() {
		t.Fatal("guest borrowed owner authorization")
	}
	for i := 0; i < 2; i++ {
		a.handleAuthenticatedCardNav("act:/autoretry on", "test:shared", "admin")
	}
	if !a.autoRetryEnabled() || b.autoRetryEnabled() {
		t.Fatal("toggle is not isolated/idempotent")
	}
	if !NewProjectStateStore(a.projectState.storePath).AutoRetryEnabled() {
		t.Fatal("setting not persisted")
	}
	a.handleCardNav("act:/autoretry off", "test:admin")
	if !a.autoRetryEnabled() {
		t.Fatal("unauthenticated callback changed setting")
	}
	a.SetDisabledCommands([]string{"autoretry"})
	a.handleAuthenticatedCardNav("act:/autoretry off", "test:admin", "admin")
	if !a.autoRetryEnabled() {
		t.Fatal("disabled command accepted")
	}
	a.SetDisabledCommands(nil)
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	a.projectState.storePath = filepath.Join(path, "state.json")
	card := a.autoRetryCommandCard([]string{"off"}, "test:admin", "admin", true)
	if !a.autoRetryEnabled() || !strings.Contains(card.RenderText(), a.i18n.T(MsgNotifySaveFailed)) {
		t.Fatal("save failure changed setting")
	}
}

func TestAutoRetryCancellationAndIdentityGuards(t *testing.T) {
	for _, kind := range []string{"disabled", "unknown-thread", "wrong-thread", "permanent", "stop", "shutdown", "cancel-at-dispatch"} {
		t.Run(kind, func(t *testing.T) {
			e, p, s := newAutoRetryEngine(t)
			r := startAutoRetryTest(t, e, p, s)
			err := errors.New("rate limit exceeded")
			switch kind {
			case "disabled":
				e.autoRetryCommandCard([]string{"off"}, "test:attention", "admin", false)
			case "unknown-thread":
				s.mu.Lock()
				s.id = ""
				s.mu.Unlock()
			case "wrong-thread":
				r.threadID = "another-thread"
			case "permanent":
				err = errors.New("invalid API key")
			case "stop", "shutdown":
				r.mu.Lock()
				r.delays[0] = time.Hour
				r.mu.Unlock()
			case "cancel-at-dispatch":
				s.prepare = func() { e.cancelAutoRetry("test:attention") }
			}
			s.events <- Event{Type: EventError, Error: err}
			if kind == "stop" || kind == "shutdown" {
				awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return !r.next.IsZero() })
				if kind == "stop" {
					e.ReceiveMessage(p, &Message{SessionKey: "test:attention", Platform: "test", UserID: "admin", Content: "/stop"})
				} else {
					e.cancel()
				}
			}
			waitAttentionIdle(t, e)
			if len(s.prompts) != 0 {
				t.Fatal("cancelled/invalid task retried")
			}
		})
	}
}

func TestAutoRetryServerMinimumOverridesImmediate(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	s.minimum = time.Hour
	r := startAutoRetryTest(t, e, p, s)
	start := time.Now()
	s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
	awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return !r.next.IsZero() })
	r.mu.Lock()
	next := r.next
	r.mu.Unlock()
	if next.Before(start.Add(time.Hour)) {
		t.Fatal("server minimum shortened")
	}
	e.cancelAutoRetry("test:attention")
	waitAttentionIdle(t, e)
}

func TestAutoRetryHeartbeatErrorsStayUnmentioned(t *testing.T) {
	s := &retryTestSession{attentionControlledSession: newAttentionControlledSession(), prompts: make(chan string, 16), id: "heartbeat-thread"}
	e, p := newHeartbeatAttentionEngine(t, &retryTestAgent{session: s})
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(t.TempDir(), "state.json")))
	if err := e.projectState.SetAutoRetryEnabled(true); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- e.ExecuteHeartbeat("test:owner", "Continue the existing task.", true) }()
	retryPrompt(t, s)
	var r *autoRetryRun
	awaitAutoRetry(t, func() bool {
		e.autoRetryMu.Lock()
		defer e.autoRetryMu.Unlock()
		r = e.autoRetryRuns["test:owner"]
		return r != nil
	})
	r.mu.Lock()
	r.delays = [3]time.Duration{}
	r.mu.Unlock()
	for i := 0; i < 3; i++ {
		s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
		retryPrompt(t, s)
	}
	s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("heartbeat did not end")
	}
	if got := p.getNotifications(); len(got) != 0 {
		t.Fatalf("heartbeat failures mentioned user: %+v", got)
	}
	if len(p.getSent()) == 0 {
		t.Fatal("heartbeat notices disappeared")
	}
}

func TestAutoRetryButtonsTranslationsAndMenu(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	for _, inPlace := range []bool{false, true} {
		card := e.autoRetryCommandCard(nil, "test:owner", "admin", inPlace)
		found := map[string]bool{}
		for _, el := range card.Elements {
			if row, ok := el.(CardActions); ok {
				for _, b := range row.Buttons {
					found[b.Value] = true
				}
			}
		}
		prefix, refresh := "cmd:", "cmd:/autoretry"
		if inPlace {
			prefix, refresh = "act:", "nav:/autoretry"
		}
		if !found[prefix+"/autoretry on"] || !found[prefix+"/autoretry off"] || !found[refresh] {
			t.Fatalf("missing buttons: %v", found)
		}
	}
	for _, lang := range []Language{LangEnglish, LangChinese, LangTraditionalChinese, LangJapanese, LangSpanish} {
		for _, key := range []MsgKey{MsgAutoRetryTitle, MsgAutoRetryStatus, MsgAutoRetryUsage, MsgAutoRetryCount, MsgAutoRetryNext, MsgAutoRetryPrompt, MsgAutoRetryScheduled, MsgAutoRetryExhausted, MsgBuiltinCmdAutoRetry} {
			if messages[key][lang] == "" {
				t.Fatalf("missing translation %s/%s", key, lang)
			}
		}
	}
	for _, cmd := range e.GetAllCommands() {
		if cmd.Command == "autoretry" {
			return
		}
	}
	t.Fatal("missing menu command")
}

func TestAutoRetryRunsAreIsolated(t *testing.T) {
	e := newNotifyTestEngine(t, "bot")
	a := e.sessions.GetOrCreateActive("test:a")
	b := e.sessions.GetOrCreateActive("test:b")
	ra := e.beginAutoRetry("test:a", a, e.sessions)
	rb := e.beginAutoRetry("test:b", b, e.sessions)
	defer ra.cancel()
	defer rb.cancel()
	ra.attempts = 2
	rb.attempts = 1
	ra.progressText("made progress")
	if ra.attempts != 0 || rb.attempts != 1 {
		t.Fatal("counter leaked across sessions")
	}
	e.cancelAutoRetry("test:a")
	if ra.ctx.Err() == nil || rb.ctx.Err() != nil {
		t.Fatal("cancellation leaked across sessions")
	}
}

func TestCUJ_AR3_QueuedUserGetsFreshBudgetAndOwnNotifications(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	startAutoRetryTest(t, e, p, s)
	e.ReceiveMessage(p, &Message{SessionKey: "test:attention", Platform: "test", UserID: "next-user", UserName: "Next", Content: "queued task", ReplyCtx: "queued-context"})
	s.events <- Event{Type: EventResult, Done: true, Content: "first done"}
	if got := retryPrompt(t, s); got != "queued task" {
		t.Fatalf("queued prompt: %q", got)
	}
	s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
	if got := retryPrompt(t, s); got != e.i18n.T(MsgAutoRetryPrompt) {
		t.Fatalf("queued continuation: %q", got)
	}
	s.events <- Event{Type: EventResult, Done: true, Content: "queued done"}
	waitAttentionIdle(t, e)
	notices := p.getNotifications()
	if len(notices) != 3 {
		t.Fatalf("notifications: %+v", notices)
	}
	for _, n := range notices[1:] {
		if n.userID != "next-user" || n.replyCtx != "queued-context" {
			t.Fatalf("retry inherited old sender: %+v", n)
		}
	}
	e.ReceiveMessage(p, &Message{SessionKey: "test:attention", Platform: "test", UserID: "admin", Content: "/autoretry off"})
	if e.autoRetryEnabled() {
		t.Fatal("off failed after queued task")
	}
}

func TestAutoRetryContinuesBeyondThreeWithProgress(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	r := startAutoRetryTest(t, e, p, s)
	for i := 0; i < 5; i++ {
		s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
		retryPrompt(t, s)
		s.events <- Event{Type: EventText, Content: "more actual work completed"}
		awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.attempts == 0 })
	}
	s.events <- Event{Type: EventResult, Done: true, Content: "done"}
	waitAttentionIdle(t, e)
}

func TestAutoRetryExternalSelectionChangeCancelsCooldown(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	r := startAutoRetryTest(t, e, p, s)
	r.mu.Lock()
	r.delays[0] = time.Hour
	r.mu.Unlock()
	s.events <- Event{Type: EventError, Error: errors.New("rate limit exceeded")}
	awaitAutoRetry(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return !r.next.IsZero() })
	original := e.sessions.GetOrCreateActive("test:attention")
	e.sessions.mu.Lock()
	e.sessions.activeSession["test:attention"] = "different-local-session"
	e.sessions.mu.Unlock()
	awaitAutoRetry(t, func() bool { return !original.Busy() })
	if len(s.prompts) != 0 {
		t.Fatal("external selection change resumed the wrong session")
	}
}

func TestAutoRetryImmediateSendFailureCanResumeKnownThread(t *testing.T) {
	e, p, s := newAutoRetryEngine(t)
	s.failSends = 1
	startAutoRetryTest(t, e, p, s)
	if got := retryPrompt(t, s); got != e.i18n.T(MsgAutoRetryPrompt) {
		t.Fatalf("wrong retry for Send failure: %q", got)
	}
	s.events <- Event{Type: EventResult, Done: true, Content: "done"}
	waitAttentionIdle(t, e)
}
