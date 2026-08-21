package core

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

type ownershipTestAgent struct {
	scope       string
	starts      []string
	startErr    error
	nextSession AgentSession
	sessions    []AgentSessionInfo
}

func (a *ownershipTestAgent) Name() string { return "ownership-test" }
func (a *ownershipTestAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.starts = append(a.starts, id)
	if a.startErr != nil {
		return nil, a.startErr
	}
	return a.nextSession, nil
}
func (a *ownershipTestAgent) ListSessions(context.Context) ([]AgentSessionInfo, error) {
	return a.sessions, nil
}
func (a *ownershipTestAgent) Stop() error { return nil }
func (a *ownershipTestAgent) SessionOwnershipScope() (string, error) {
	return a.scope, nil
}
func (a *ownershipTestAgent) IsSessionResumeConflict(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "active writer")
}

func newOwnershipTestEngine(t *testing.T, project string, agent Agent) (*Engine, string) {
	t.Helper()
	root := t.TempDir()
	e := NewEngine(project, agent, nil, filepath.Join(root, project+"-sessions.json"), LangEnglish)
	e.SetDataDir(filepath.Join(root, "data"))
	return e, root
}

func TestInteractiveResume_OwnershipConflictPreservesIDAndDoesNotStart(t *testing.T) {
	agent := &ownershipTestAgent{scope: filepath.Join(t.TempDir(), ".codex")}
	e, root := newOwnershipTestEngine(t, "project-b", agent)
	key := "test:chat:user-b"
	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("thread-1", agent.Name())
	e.sessions.Save()

	err := e.sessionOwnershipRegistry.Claim(agent.scope, "thread-1", AgentSessionOwner{
		Project:    "project-a",
		StorePath:  filepath.Join(root, "project-a-sessions.json"),
		SessionKey: "test:chat:user-a",
	})
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	state := e.getOrCreateInteractiveStateWith(key, platformNameOnly{name: "test"}, nil, session, e.sessions, nil, "")
	if state.agentSession != nil {
		t.Fatal("ownership conflict started an agent session")
	}
	if len(agent.starts) != 0 {
		t.Fatalf("StartSession calls = %v, want none", agent.starts)
	}
	if got := session.GetAgentSessionID(); got != "thread-1" {
		t.Fatalf("AgentSessionID = %q, want preserved thread-1", got)
	}
	if !strings.Contains(state.startupMessage, "project-a") {
		t.Fatalf("startupMessage = %q, want existing owner", state.startupMessage)
	}
}

func TestInteractiveResume_ActiveWriterDoesNotFallbackOrClearID(t *testing.T) {
	busyErr := errors.New("thread-store conflict: thread thread-1 already has an active writer")
	agent := &ownershipTestAgent{scope: filepath.Join(t.TempDir(), ".codex"), startErr: busyErr}
	e, _ := newOwnershipTestEngine(t, "project-a", agent)
	key := "test:chat:user-a"
	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("thread-1", agent.Name())
	e.sessions.Save()

	state := e.getOrCreateInteractiveStateWith(key, platformNameOnly{name: "test"}, nil, session, e.sessions, nil, "")
	if state.agentSession != nil {
		t.Fatal("active-writer conflict unexpectedly started a session")
	}
	if len(agent.starts) != 1 || agent.starts[0] != "thread-1" {
		t.Fatalf("StartSession calls = %v, want [thread-1] without fresh fallback", agent.starts)
	}
	if got := session.GetAgentSessionID(); got != "thread-1" {
		t.Fatalf("AgentSessionID = %q, want preserved thread-1", got)
	}
	if !strings.Contains(strings.ToLower(state.startupMessage), "active writer") {
		t.Fatalf("startupMessage = %q, want active writer guidance", state.startupMessage)
	}
}

func TestInteractiveWriteback_ConflictingNewThreadIsClosedAndNotPersisted(t *testing.T) {
	live := newControllableSession("thread-claimed-elsewhere")
	agent := &ownershipTestAgent{scope: filepath.Join(t.TempDir(), ".codex"), nextSession: live}
	e, root := newOwnershipTestEngine(t, "project-b", agent)
	key := "test:chat:user-b"
	session := e.sessions.GetOrCreateActive(key)

	if err := e.sessionOwnershipRegistry.Claim(agent.scope, "thread-claimed-elsewhere", AgentSessionOwner{
		Project:    "project-a",
		StorePath:  filepath.Join(root, "project-a-sessions.json"),
		SessionKey: "test:chat:user-a",
	}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	state := e.getOrCreateInteractiveStateWith(key, platformNameOnly{name: "test"}, nil, session, e.sessions, nil, "")
	if state.agentSession != nil {
		t.Fatal("conflicting writeback retained an unowned live session")
	}
	if got := session.GetAgentSessionID(); got != "" {
		t.Fatalf("AgentSessionID = %q, want empty", got)
	}
	select {
	case <-live.closed:
	default:
		t.Fatal("conflicting live session was not closed")
	}
}

func TestHandleRelay_OwnershipConflictDoesNotResumeOrFallback(t *testing.T) {
	agent := &ownershipTestAgent{scope: filepath.Join(t.TempDir(), ".codex")}
	e, root := newOwnershipTestEngine(t, "target", agent)
	relayKey := "relay:source:test:chat-1"
	session := e.sessions.GetOrCreateActive(relayKey)
	session.SetAgentSessionID("thread-1", agent.Name())
	e.sessions.Save()

	if err := e.sessionOwnershipRegistry.Claim(agent.scope, "thread-1", AgentSessionOwner{
		Project:    "other",
		StorePath:  filepath.Join(root, "other.json"),
		SessionKey: "relay:other:test:chat-2",
	}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	_, err := e.HandleRelay(context.Background(), "source", "test:chat-1:user", "hello")
	if !IsSessionOwnershipConflict(err) {
		t.Fatalf("HandleRelay() error = %v, want ownership conflict", err)
	}
	if len(agent.starts) != 0 {
		t.Fatalf("StartSession calls = %v, want none", agent.starts)
	}
	if got := session.GetAgentSessionID(); got != "thread-1" {
		t.Fatalf("AgentSessionID = %q, want preserved thread-1", got)
	}
}

func TestCardSwitch_OwnershipConflictLeavesActiveSessionUnchanged(t *testing.T) {
	agent := &ownershipTestAgent{
		scope:    filepath.Join(t.TempDir(), ".codex"),
		sessions: []AgentSessionInfo{{ID: "thread-target", Summary: "target"}},
	}
	e, root := newOwnershipTestEngine(t, "project-b", agent)
	key := "test:chat:user-b"
	active := e.sessions.GetOrCreateActive(key)
	active.SetAgentSessionID("thread-current", agent.Name())
	e.sessions.Save()

	if err := e.sessionOwnershipRegistry.Claim(agent.scope, "thread-target", AgentSessionOwner{
		Project:    "project-a",
		StorePath:  filepath.Join(root, "project-a.json"),
		SessionKey: "test:chat:user-a",
	}); err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	card := e.handleSwitchCardAction("1", key)
	if card == nil || !strings.Contains(card.RenderText(), "project-a") {
		t.Fatalf("switch conflict card = %#v, want existing owner detail", card)
	}
	if got := e.sessions.GetOrCreateActive(key).GetAgentSessionID(); got != "thread-current" {
		t.Fatalf("active AgentSessionID = %q, want thread-current", got)
	}
}
