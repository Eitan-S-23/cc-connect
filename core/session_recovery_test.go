package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type recoveryTestAgent struct {
	mu       sync.Mutex
	context  SessionRecoveryContext
	contexts map[string]SessionRecoveryContext
	err      error
	starts   []string
	live     []*recoveryTestSession
	sessions []AgentSessionInfo
}

type recoveryTestSession struct {
	*cujAgentSession
	id string
}

func (s *recoveryTestSession) CurrentSessionID() string { return s.id }
func (a *recoveryTestAgent) Name() string               { return "recovery-test" }
func (a *recoveryTestAgent) Stop() error                { return nil }
func (a *recoveryTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]AgentSessionInfo(nil), a.sessions...), nil
}
func (a *recoveryTestAgent) CanRecoverSession(err error) bool {
	return err != nil && strings.Contains(err.Error(), "invalid_encrypted_content")
}
func (a *recoveryTestAgent) GetSessionRecoveryContext(_ context.Context, id string) (SessionRecoveryContext, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.contexts != nil {
		if ctx, ok := a.contexts[id]; ok {
			return ctx, a.err
		}
		return SessionRecoveryContext{}, errors.New("session history not found for " + id)
	}
	return a.context, a.err
}
func (a *recoveryTestAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.starts = append(a.starts, id)
	s := &recoveryTestSession{cujAgentSession: newCUJAgentSession(), id: "fresh-thread"}
	if id != "" {
		s.id = id
		s.pendingEvents = []Event{{Type: EventError, Error: errors.New("400 invalid_encrypted_content")}}
	}
	a.live = append(a.live, s)
	return s, nil
}

func newRecoveryTestEngine(t *testing.T, agent Agent) (*Engine, *stubPlatformEngine, *Session) {
	t.Helper()
	p := &stubPlatformEngine{n: "test"}
	e := NewEngine("test", agent, []Platform{p}, filepath.Join(t.TempDir(), "sessions.json"), LangEnglish)
	t.Cleanup(func() { _ = e.Stop() })
	s := e.sessions.GetOrCreateActive("test:user")
	s.SetAgentSessionID("broken-thread", agent.Name())
	s.SetActiveProvider("original-provider")
	s.AddHistory("user", "original task")
	e.sessions.Save()
	return e, p, s
}

func recoveryUserMessage(content string) *Message {
	return &Message{SessionKey: "test:user", Platform: "test", UserID: "user", UserName: "user",
		MessageID: "msg-" + content, ReplyCtx: "ctx", Content: content}
}

func TestRecoverySessionPersistsContextAndOriginalBinding(t *testing.T) {
	a := &recoveryTestAgent{context: SessionRecoveryContext{Text: "readable prior work", Truncated: true}}
	e, p, source := newRecoveryTestEngine(t, a)
	e.ReceiveMessage(p, recoveryUserMessage("/recover broken-thread"))
	active := e.sessions.GetOrCreateActive("test:user")
	if active == source || active.GetAgentSessionID() != "" {
		t.Fatal("recovery must select a fresh unbound session")
	}
	if source.GetAgentSessionID() != "broken-thread" || source.HistoryLen() != 1 {
		t.Fatal("original binding or history was changed")
	}
	reloaded := NewSessionManager(e.sessions.storePath).GetOrCreateActive("test:user")
	if reloaded.RecoveryContext != a.context.Text || reloaded.RecoveredFrom != "broken-thread" ||
		reloaded.GetActiveProvider() != "original-provider" {
		t.Fatal("recovery context, provenance or provider did not survive reload")
	}
	if !strings.Contains(strings.Join(p.getSent(), "\n"), e.i18n.T(MsgRecoveryTruncated)) {
		t.Fatal("missing truncation disclosure")
	}
	if len(a.starts) != 0 {
		t.Fatal("recover must not invoke the model or replay a task")
	}
	e.ReceiveMessage(p, recoveryUserMessage("/recover broken-thread"))
	if e.sessions.GetOrCreateActive("test:user") != active {
		t.Fatal("duplicate old button changed the active session")
	}
}

func TestRecoveryFailureDoesNotSwitchOrEraseHistory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result SessionRecoveryContext
		err    error
	}{
		{name: "read error", err: errors.New("parent history missing")},
		{name: "empty export"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &recoveryTestAgent{context: tc.result, err: tc.err}
			e, p, source := newRecoveryTestEngine(t, a)
			e.ReceiveMessage(p, recoveryUserMessage("/recover"))
			if e.sessions.GetOrCreateActive("test:user") != source || source.HistoryLen() != 1 {
				t.Fatal("failed recovery changed the current session")
			}
		})
	}
}

func TestRecoverySessionRejectsSelectionOrBindingChangedDuringExport(t *testing.T) {
	for _, mode := range []string{"selection", "binding"} {
		t.Run(mode, func(t *testing.T) {
			e, _, source := newRecoveryTestEngine(t, &recoveryTestAgent{})
			if mode == "selection" {
				e.sessions.NewSession("test:user", "other conversation")
			} else {
				source.SetAgentSessionID("different-thread", "recovery-test")
			}
			current := e.sessions.GetOrCreateActive("test:user")
			target := recoveryTarget{ID: "broken-thread", Name: source.GetName(), Provider: source.GetActiveProvider()}
			if e.sessions.newRecoverySession("test:user", source, "broken-thread", target, "prior work") != nil ||
				e.sessions.GetOrCreateActive("test:user") != current {
				t.Fatal("recovery overrode a newer session selection or binding")
			}
		})
	}
}

func TestRecoveryPromptIncludesProvenanceWithoutConsumingContext(t *testing.T) {
	s := &Session{RecoveryContext: "pending", RecoveredFrom: "original"}
	if !strings.Contains(s.withRecoveryContext("inspect only"), "Source session ID: original") {
		t.Fatal("recovery prompt lost its provenance")
	}
	if s.withRecoveryContext("inspect only") == "inspect only" {
		t.Fatal("preparing a request consumed pending context")
	}
}

func TestRecoveryPendingContextSurvivesIdleRotation(t *testing.T) {
	e, p, source := newRecoveryTestEngine(t, &recoveryTestAgent{})
	e.resetOnIdle = time.Minute
	source.mu.Lock()
	source.RecoveryContext = "pending context after failed first turn"
	source.LastUserActivity = time.Now().Add(-time.Hour)
	source.mu.Unlock()
	if e.maybeAutoResetSessionOnIdle(p, recoveryUserMessage("continue"), e.sessions, "test:user", source) != nil ||
		e.sessions.GetOrCreateActive("test:user") != source {
		t.Fatal("idle rotation discarded pending recovery context")
	}
}

func TestRecoveryRejectsBusyStaleAndDisabledRequests(t *testing.T) {
	for _, mode := range []string{"busy", "stale", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			a := &recoveryTestAgent{context: SessionRecoveryContext{Text: "prior work"}}
			e, p, source := newRecoveryTestEngine(t, a)
			command := "/recover"
			switch mode {
			case "busy":
				source.TryLock()
				defer source.Unlock()
			case "stale":
				command += " another-users-thread"
			case "disabled":
				e.SetDisabledCommands([]string{"recover"})
			}
			e.ReceiveMessage(p, recoveryUserMessage(command))
			if e.sessions.GetOrCreateActive("test:user") != source {
				t.Fatal("rejected recovery switched the session")
			}
		})
	}
}

func TestRecoveryOfferOnlyForSupportedErrorAndIncludesPinnedButton(t *testing.T) {
	a := &recoveryTestAgent{}
	e, _, source := newRecoveryTestEngine(t, a)
	p := &stubCardPlatform{stubPlatformEngine: stubPlatformEngine{n: "cards"}}
	if e.sendSessionRecoveryOffer(p, "ctx", a, source, "rate limited", errors.New("429")) {
		t.Fatal("ordinary errors must not suggest context recovery")
	}
	if !e.sendSessionRecoveryOffer(p, "ctx", a, source, "400", errors.New("invalid_encrypted_content")) {
		t.Fatal("missing encrypted-history recovery offer")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.sentCards) != 1 {
		t.Fatalf("got %d recovery cards", len(p.sentCards))
	}
	for _, element := range p.sentCards[0].Elements {
		if actions, ok := element.(CardActions); ok && len(actions.Buttons) == 1 &&
			actions.Buttons[0].Value == "cmd:/recover broken-thread" {
			return
		}
	}
	t.Fatal("recovery card lacks a button pinned to the failed session")
}

func TestRecoveryContextSurvivesErrorUntilSuccessfulTurn(t *testing.T) {
	a := &recoveryTestAgent{}
	e, p, source := newRecoveryTestEngine(t, a)
	source.RecoveryContext = "pending recovered context"
	e.ReceiveMessage(p, recoveryUserMessage("try again"))
	waitAttentionSessionIdle(t, e, "test:user")
	if !strings.Contains(source.withRecoveryContext("next"), "pending recovered context") {
		t.Fatal("failed turn discarded pending context")
	}
	a.mu.Lock()
	s := a.live[0]
	a.mu.Unlock()
	s.mu.Lock()
	s.pendingEvents = []Event{{Type: EventResult, Content: "completed", Done: true}}
	s.mu.Unlock()
	e.ReceiveMessage(p, recoveryUserMessage("retry safely"))
	waitAttentionSessionIdle(t, e, "test:user")
	if source.withRecoveryContext("next") != "next" {
		t.Fatal("successful turn did not release the pending copy")
	}
}

// 选项 3：/recover 接受 /list 行号、ID 前缀或会话名，不再只认当前活跃会话。
func TestRecoveryTargetsSessionChosenFromList(t *testing.T) {
	older := AgentSessionInfo{ID: "older-thread", Summary: "older work", MessageCount: 4, ModifiedAt: time.Now()}
	newer := AgentSessionInfo{ID: "newer-thread", Summary: "newer work", MessageCount: 2, ModifiedAt: time.Now()}
	a := &recoveryTestAgent{
		contexts: map[string]SessionRecoveryContext{
			"broken-thread": {Text: "active context"},
			"older-thread":  {Text: "older readable work"},
		},
		sessions: []AgentSessionInfo{older, newer},
	}
	for _, query := range []string{"1", "older", "older-thread"} {
		t.Run(query, func(t *testing.T) {
			e, p, source := newRecoveryTestEngine(t, a)
			e.ReceiveMessage(p, recoveryUserMessage("/recover "+query))
			active := e.sessions.GetOrCreateActive("test:user")
			if active == source {
				t.Fatalf("/recover %q did not select a fresh session", query)
			}
			if active.RecoveredFrom != "older-thread" || active.RecoveryContext != "older readable work" {
				t.Fatalf("/recover %q recovered %q with %q", query, active.RecoveredFrom, active.RecoveryContext)
			}
			if source.GetAgentSessionID() != "broken-thread" || e.sessions.FindLocalByAgentSessionID("test:user", "broken-thread") != source {
				t.Fatal("targeting another session changed the original binding")
			}
			if !strings.Contains(strings.Join(p.getSent(), "\n"), "older-thread") {
				t.Fatal("recovery receipt does not name the recovered source session")
			}
		})
	}
}

func TestRecoveryRejectsUnknownOrMismatchedTarget(t *testing.T) {
	a := &recoveryTestAgent{
		contexts: map[string]SessionRecoveryContext{"broken-thread": {Text: "active context"}},
		sessions: []AgentSessionInfo{{ID: "other-thread", Summary: "other"}},
	}
	e, p, source := newRecoveryTestEngine(t, a)
	e.ReceiveMessage(p, recoveryUserMessage("/recover no-such-session"))
	if e.sessions.GetOrCreateActive("test:user") != source {
		t.Fatal("unknown target switched the session")
	}
	if !strings.Contains(strings.Join(p.getSent(), "\n"), "no-such-session") {
		t.Fatal("unknown target was not reported back to the user")
	}
}

func TestRecoveryModeCardOffersPerRowRecoverButton(t *testing.T) {
	a := &recoveryTestAgent{
		contexts: map[string]SessionRecoveryContext{"broken-thread": {Text: "active context"}},
		sessions: []AgentSessionInfo{
			{ID: "broken-thread", Summary: "current work"},
			{ID: "older-thread", Summary: "older work"},
		},
	}
	e, _, _ := newRecoveryTestEngine(t, a)
	card := e.handleCardNav("nav:/list-recover 1", "test:user")
	if card == nil {
		t.Fatal("recovery mode card was not rendered")
	}
	if got := countCardActionValues(card, "cmd:/recover "); got != 2 {
		t.Fatalf("recover buttons = %d, want 2", got)
	}
	if _, ok := findCardAction(card, "cmd:/recover older-thread"); !ok {
		t.Fatal("older session has no recover button")
	}
	if _, ok := findCardAction(card, "nav:/list 1"); !ok {
		t.Fatal("recovery mode card has no way back to the session list")
	}
}

// /list 进入恢复模式、点某行恢复、再回到列表：全过程按用户可见内容断言。
func TestCUJ_B2_RecoverSessionChosenInList(t *testing.T) {
	a := &recoveryTestAgent{
		contexts: map[string]SessionRecoveryContext{
			"broken-thread": {Text: "active readable work"},
			"older-thread":  {Text: "[user]\nolder requirement\n[assistant]\npartial result"},
		},
		sessions: []AgentSessionInfo{
			{ID: "broken-thread", Summary: "current work", MessageCount: 3, ModifiedAt: time.Now()},
			{ID: "older-thread", Summary: "older work", MessageCount: 5, ModifiedAt: time.Now()},
		},
	}
	e, p, source := newRecoveryTestEngine(t, a)
	listCard, err := e.renderListCard("test:user", 1)
	if err != nil {
		t.Fatalf("renderListCard: %v", err)
	}
	if _, ok := findCardAction(listCard, "nav:/list-recover 1"); !ok {
		t.Fatal("session list has no entry into recovery mode")
	}
	e.ReceiveMessage(p, recoveryUserMessage("/recover older-thread"))
	recovered := e.sessions.GetOrCreateActive("test:user")
	if recovered == source || recovered.RecoveredFrom != "older-thread" {
		t.Fatal("list-driven recovery did not target the chosen session")
	}
	e.ReceiveMessage(p, recoveryUserMessage("continue from the older work"))
	waitRecovery(t, func() bool {
		return recovered.GetAgentSessionID() == "fresh-thread" && !recovered.Busy()
	})
	a.mu.Lock()
	fresh := a.live[len(a.live)-1]
	a.mu.Unlock()
	prompts := fresh.getSentPrompts()
	if len(prompts) != 1 || !strings.Contains(prompts[0], "older requirement") ||
		!strings.Contains(prompts[0], "Source session ID: older-thread") {
		t.Fatal("next turn lost the recovered context of the chosen session")
	}
	if source.GetAgentSessionID() != "broken-thread" || source.HistoryLen() != 1 {
		t.Fatal("recovering another session modified the original session")
	}
}

func waitRecovery(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("recovery condition timed out")
}
