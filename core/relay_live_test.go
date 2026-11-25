package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type liveTestContext struct {
	key     string
	private bool
}

type liveTestPlatform struct {
	stubPlatformEngine
	keys []string
	fail bool
}

func (p *liveTestPlatform) IsPrivateChat(ctx any) bool {
	c, ok := ctx.(liveTestContext)
	return ok && c.private
}
func (p *liveTestPlatform) ReconstructReplyCtx(key string) (any, error) {
	return liveTestContext{key: key}, nil
}
func (p *liveTestPlatform) Send(ctx context.Context, rctx any, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fail {
		return errors.New("delivery unavailable")
	}
	p.sent = append(p.sent, text)
	if c, ok := rctx.(liveTestContext); ok {
		p.keys = append(p.keys, c.key)
	}
	return nil
}

type liveTestAgent struct {
	stubAgent
	session *liveTestSession
	starts  atomic.Int32
	resume  string
}

func (a *liveTestAgent) StartSession(_ context.Context, id string) (AgentSession, error) {
	a.resume = id
	a.starts.Add(1)
	return a.session, nil
}

type liveTestSession struct {
	stubAgentSession
	events      chan Event
	prompts     chan string
	permissions chan PermissionResult
	closed      atomic.Bool
}

func (s *liveTestSession) Send(text, _ string, _ []ImageAttachment, _ []FileAttachment) error {
	s.prompts <- text
	return nil
}
func (s *liveTestSession) Events() <-chan Event { return s.events }
func (s *liveTestSession) Alive() bool          { return !s.closed.Load() }
func (s *liveTestSession) Close() error         { s.closed.Store(true); return nil }
func (s *liveTestSession) RespondPermission(_ string, result PermissionResult) error {
	s.permissions <- result
	return nil
}

type liveFixture struct {
	rm     *RelayManager
	a, b   *Engine
	pa, pb *liveTestPlatform
	agent  *liveTestAgent
	as     *liveTestSession
}

func newLiveFixture(t *testing.T) *liveFixture {
	t.Helper()
	f := &liveFixture{rm: NewRelayManager(t.TempDir())}
	f.pa = &liveTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	f.pb = &liveTestPlatform{stubPlatformEngine: stubPlatformEngine{n: "test"}}
	f.as = &liveTestSession{events: make(chan Event, 20), prompts: make(chan string, 10), permissions: make(chan PermissionResult, 4)}
	f.agent = &liveTestAgent{session: f.as}
	f.a = NewEngine("a", &stubAgent{}, []Platform{f.pa}, "", LangEnglish)
	f.b = NewEngine("b", f.agent, []Platform{f.pb}, "", LangEnglish)
	for _, e := range []*Engine{f.a, f.b} {
		e.SetAdminFrom("admin")
		e.SetRelayManager(f.rm)
		f.rm.RegisterEngine(e.name, e)
		e.platformReady[e.platforms[0]] = true
		e.SetStreamPreviewCfg(StreamPreviewCfg{})
		t.Cleanup(e.cancel)
	}
	t.Cleanup(func() {
		f.a.cancel()
		f.b.cancel()
		liveWait(t, func() bool {
			f.b.relayActivity.mu.Lock()
			defer f.b.relayActivity.mu.Unlock()
			return f.b.relayActivity.active == 0 && !f.b.relayActivity.reserved
		})
	})
	return f
}

func liveWait(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not become true")
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *liveFixture) command(e *Engine, p *liveTestPlatform, chat, text string) {
	key := "test:" + chat + ":admin"
	e.ReceiveMessage(p, &Message{SessionKey: key, Platform: "test", UserID: "admin", Content: text, ReplyCtx: liveTestContext{key, true}})
}

func (f *liveFixture) setup() {
	f.command(f.b, f.pb, "b-chat", "/bind here")
	f.command(f.a, f.pa, "a-chat", "/relay on")
	f.command(f.a, f.pa, "a-chat", "/bind b")
}

func (f *liveFixture) request() RelayRequest {
	return RelayRequest{From: "a", To: "b", SessionKey: "test:a-chat:admin", Message: "delegated task"}
}

func (f *liveFixture) send() <-chan relayOutcome {
	done := make(chan relayOutcome, 1)
	go func() {
		response, err := f.rm.Send(context.Background(), f.request())
		text := ""
		if response != nil {
			text = response.Response
		}
		done <- relayOutcome{text, err}
	}()
	return done
}

func liveAwaitPrompt(t *testing.T, s *liveTestSession) string {
	t.Helper()
	select {
	case prompt := <-s.prompts:
		return prompt
	case <-time.After(3 * time.Second):
		t.Fatal("agent did not receive task")
		return ""
	}
}

func liveAwaitResult(t *testing.T, done <-chan relayOutcome) relayOutcome {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(3 * time.Second):
		t.Fatal("relay did not complete")
		return relayOutcome{}
	}
}

func runLivePrivateJourney(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	session := f.b.sessions.GetOrCreateActive("test:b-chat:admin")
	session.SetAgentSessionID("existing-conversation", "stub")
	session.AddHistory("user", "previous task")
	done := f.send()
	if prompt := liveAwaitPrompt(t, f.as); !strings.Contains(prompt, "delegated task") {
		t.Fatal(prompt)
	}
	if f.agent.resume != "existing-conversation" {
		t.Fatalf("did not resume B: %q", f.agent.resume)
	}
	f.as.events <- Event{Type: EventText, Content: "Working on the requested change."}
	f.as.events <- Event{Type: EventToolUse, ToolName: "Read", ToolInput: "source.go"}
	liveWait(t, func() bool {
		return strings.Contains(strings.Join(f.pb.getSent(), "\n"), "Working on the requested change.")
	})
	select {
	case <-done:
		t.Fatal("relay returned before task finished")
	default:
	}
	f.as.events <- Event{Type: EventResult, Content: "task complete", Done: true}
	result := liveAwaitResult(t, done)
	if result.err != nil || result.text != "task complete" {
		t.Fatalf("result: %+v", result)
	}
	f.pb.mu.Lock()
	for _, key := range f.pb.keys {
		if key != "test:b-chat:admin" {
			t.Errorf("B output leaked to %q", key)
		}
	}
	f.pb.mu.Unlock()
	if strings.Contains(strings.Join(f.pa.getSent(), "\n"), "Working on") {
		t.Fatal("B progress leaked into A chat")
	}
	history := session.GetHistory(0)
	if len(history) != 3 || history[0].Content != "previous task" || history[2].Content != "task complete" {
		t.Fatalf("history: %+v", history)
	}
}

func TestRelayLiveRejectsBusyAcrossAllSessionManagers(t *testing.T) {
	for _, kind := range []string{"normal", "workspace", "side", "closing", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			f := newLiveFixture(t)
			f.setup()
			var release func()
			switch kind {
			case "closing":
				release = f.b.beginSessionClose("other-chat")
			case "legacy":
				if !f.b.relayActivity.start(false) {
					t.Fatal("could not start legacy task")
				}
				release = f.b.relayActivity.finish
			default:
				sm := f.b.sessions
				if kind == "workspace" {
					sm = NewSessionManager("", f.b.relayActivity)
				}
				s := sm.GetOrCreateActive("another-chat")
				if kind == "side" {
					s = sm.NewSideSession("another-chat", "cron")
				}
				if !s.TryLock() {
					t.Fatal("could not start existing task")
				}
				release = s.Unlock
			}
			defer release()
			_, err := f.rm.Send(context.Background(), f.request())
			if err == nil || !strings.Contains(err.Error(), "busy") {
				t.Fatalf("expected busy: %v", err)
			}
			if f.agent.starts.Load() != 0 || len(f.as.prompts) != 0 {
				t.Fatal("busy target started a task")
			}
		})
	}
}

func TestRelayLiveConcurrentDispatchExactlyOneAccepted(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	const n = 12
	results := make(chan relayOutcome, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		go func() {
			<-start
			resp, err := f.rm.Send(context.Background(), f.request())
			text := ""
			if resp != nil {
				text = resp.Response
			}
			results <- relayOutcome{text, err}
		}()
	}
	close(start)
	liveAwaitPrompt(t, f.as)
	for i := 0; i < n-1; i++ {
		r := liveAwaitResult(t, results)
		if r.err == nil || !strings.Contains(r.err.Error(), "busy") {
			t.Fatalf("unexpected contender: %+v", r)
		}
	}
	f.as.events <- Event{Type: EventResult, Content: "winner", Done: true}
	if r := liveAwaitResult(t, results); r.err != nil || r.text != "winner" {
		t.Fatalf("winner: %+v", r)
	}
	if f.agent.starts.Load() != 1 {
		t.Fatal("multiple agents started")
	}
}

func TestRelayLiveSwitchIsGlobalPersistentAndDoesNotInterrupt(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	done := f.send()
	liveAwaitPrompt(t, f.as)
	f.command(f.b, f.pb, "b-chat", "/relay off")
	if f.a.relayManager.LiveEnabled() || f.b.relayManager.LiveEnabled() {
		t.Fatal("switch not global")
	}
	loaded := NewRelayManager(filepath.Dir(f.rm.livePath))
	loaded.SetLiveDefault(true)
	if loaded.LiveEnabled() {
		t.Fatal("explicit off lost after restart")
	}
	f.as.events <- Event{Type: EventText, Content: "still working"}
	f.as.events <- Event{Type: EventToolUse, ToolName: "Read", ToolInput: "file"}
	liveWait(t, func() bool { return strings.Contains(strings.Join(f.pb.getSent(), "\n"), "still working") })
	f.as.events <- Event{Type: EventResult, Content: "finished after toggle", Done: true}
	if r := liveAwaitResult(t, done); r.err != nil || r.text != "finished after toggle" {
		t.Fatalf("result: %+v", r)
	}
	if len(loaded.liveTargets("a", "test:a-chat:admin")) != 1 {
		t.Fatal("bindings lost")
	}
}

func TestRelayLiveWaitTimeoutKeepsTaskAndBusyReservation(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	f.rm.SetTimeout(40 * time.Millisecond)
	done := f.send()
	liveAwaitPrompt(t, f.as)
	if r := liveAwaitResult(t, done); r.err != nil || !strings.Contains(r.text, "Task accepted") {
		t.Fatalf("timeout: %+v", r)
	}
	if _, err := f.rm.Send(context.Background(), f.request()); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("lost reservation: %v", err)
	}
	f.as.events <- Event{Type: EventResult, Content: "late completion", Done: true}
	liveWait(t, func() bool { return strings.Contains(strings.Join(f.pb.getSent(), "\n"), "late completion") })
}

func TestRelayLivePermissionsStayInteractiveAndBusy(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	done := f.send()
	liveAwaitPrompt(t, f.as)
	f.as.events <- Event{Type: EventPermissionRequest, RequestID: "permission-1", ToolName: "Bash", ToolInput: "touch output"}
	liveWait(t, func() bool { return strings.Contains(strings.Join(f.pb.getSent(), "\n"), "Bash") })
	select {
	case <-f.as.permissions:
		t.Fatal("delegation auto-approved permission")
	default:
	}
	if _, err := f.rm.Send(context.Background(), f.request()); err == nil {
		t.Fatal("accepted while permission pending")
	}
	f.command(f.b, f.pb, "b-chat", "allow")
	select {
	case result := <-f.as.permissions:
		if result.Behavior != "allow" {
			t.Fatal(result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("B permission reply did not reach task")
	}
	f.as.events <- Event{Type: EventResult, Content: "approved completion", Done: true}
	if r := liveAwaitResult(t, done); r.err != nil {
		t.Fatal(r.err)
	}
}

func TestRelayLiveRoutingAndAuthorizationFailClosed(t *testing.T) {
	for _, kind := range []string{"unbound", "wrong-source-chat", "group", "guest", "moved-inbox", "revoked-admin", "offline", "delivery-failed"} {
		t.Run(kind, func(t *testing.T) {
			f := newLiveFixture(t)
			f.setup()
			req := f.request()
			switch kind {
			case "unbound":
				req.To = "missing"
			case "wrong-source-chat":
				req.SessionKey = "test:other:admin"
			case "group", "guest":
				key := "test:new-chat:admin"
				user := "admin"
				if kind == "guest" {
					user = "guest"
				}
				f.b.ReceiveMessage(f.pb, &Message{SessionKey: key, Platform: "test", UserID: user, Content: "/bind here", ReplyCtx: liveTestContext{key, kind != "group"}})
				if f.rm.live.Inboxes["b"].SessionKey != "test:b-chat:admin" {
					t.Fatal("unauthorized inbox change")
				}
				return
			case "moved-inbox":
				f.command(f.b, f.pb, "new-b-chat", "/bind here")
			case "revoked-admin":
				f.b.SetAdminFrom("someone-else")
			case "offline":
				f.b.platformReady[f.pb] = false
			case "delivery-failed":
				f.pb.fail = true
			}
			if _, err := f.rm.Send(context.Background(), req); err == nil {
				t.Fatal("accepted invalid route")
			}
			if f.agent.starts.Load() != 0 {
				t.Fatal("invalid task reached agent")
			}
		})
	}
}

func TestRelayLiveGlobalSwitchActorAndPersistenceFailure(t *testing.T) {
	f := newLiveFixture(t)
	for _, actor := range []string{"guest", ""} {
		card := f.a.handleAuthenticatedCardNav("act:/relay on", "test:chat:admin", actor)
		if card == nil || f.rm.LiveEnabled() {
			t.Fatal("unauthorized switch")
		}
	}
	card := f.a.handleAuthenticatedCardNav("act:/relay on", "test:chat:admin", "admin")
	if card == nil || !f.b.relayManager.LiveEnabled() {
		t.Fatal("authorized global switch failed")
	}
	if err := os.Remove(f.rm.livePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.rm.livePath, 0o700); err != nil {
		t.Fatal(err)
	}
	card = f.a.relayCommandCard([]string{"off"}, "admin", true)
	if !f.rm.LiveEnabled() || !strings.Contains(card.RenderText(), "Could not save") {
		t.Fatal("failed save changed runtime")
	}
}

func TestRelayActivityAdmissionRace(t *testing.T) {
	for i := 0; i < 100; i++ {
		a := &relayActivity{}
		s := NewSessionManager("", a).GetOrCreateActive("normal")
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var normal, reserved bool
		go func() { defer wg.Done(); <-start; normal = s.TryLock() }()
		go func() { defer wg.Done(); <-start; reserved = a.reserve() }()
		close(start)
		wg.Wait()
		if normal == reserved {
			t.Fatalf("admission normal=%v relay=%v", normal, reserved)
		}
		if normal {
			s.Unlock()
		} else {
			a.release()
		}
	}
}

func TestRelayLiveStopAndErrorReleaseBusyState(t *testing.T) {
	for _, kind := range []string{"stop", "error"} {
		t.Run(kind, func(t *testing.T) {
			f := newLiveFixture(t)
			f.setup()
			done := f.send()
			liveAwaitPrompt(t, f.as)
			if kind == "stop" {
				f.command(f.b, f.pb, "b-chat", "/stop")
			} else {
				f.as.events <- Event{Type: EventError, Error: errors.New("failed task")}
			}
			if r := liveAwaitResult(t, done); r.err == nil {
				t.Fatal("interrupted task reported success")
			}
			liveWait(t, func() bool {
				f.b.relayActivity.mu.Lock()
				defer f.b.relayActivity.mu.Unlock()
				return f.b.relayActivity.active == 0 && !f.b.relayActivity.reserved
			})
			if !f.b.sessions.GetOrCreateActive("other").TryLock() {
				t.Fatal("target left permanently busy")
			}
			f.b.sessions.GetOrCreateActive("other").Unlock()
		})
	}
}

func TestRelayLiveResultDoesNotIncludeQueuedUserTurn(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	done := f.send()
	liveAwaitPrompt(t, f.as)
	f.command(f.b, f.pb, "b-chat", "follow-up from B user")
	f.as.events <- Event{Type: EventResult, Content: "delegated result", Done: true}
	if r := liveAwaitResult(t, done); r.err != nil || r.text != "delegated result" {
		t.Fatalf("wrong delegated result: %+v", r)
	}
	if prompt := liveAwaitPrompt(t, f.as); !strings.Contains(prompt, "follow-up") {
		t.Fatal(prompt)
	}
	f.as.events <- Event{Type: EventResult, Content: "private follow-up answer", Done: true}
	liveWait(t, func() bool { return !f.b.sessions.GetOrCreateActive("test:b-chat:admin").Busy() })
	if f.b.relayActivity.isReserved() {
		t.Fatal("reservation survived session unlock")
	}
}

func TestRelayLiveOtherChatsAreRejectedWithoutOrphanQueues(t *testing.T) {
	f := newLiveFixture(t)
	f.setup()
	done := f.send()
	liveAwaitPrompt(t, f.as)
	f.command(f.b, f.pb, "other-chat", "different conversation")
	if !strings.Contains(strings.Join(f.pb.getSent(), "\n"), "Target is busy") {
		t.Fatal("other chat was not rejected")
	}
	f.as.events <- Event{Type: EventResult, Content: "done", Done: true}
	liveAwaitResult(t, done)
	liveWait(t, func() bool { return !f.b.sessions.GetOrCreateActive("test:b-chat:admin").Busy() })
	select {
	case prompt := <-f.as.prompts:
		t.Fatalf("rejected prompt was queued: %q", prompt)
	default:
	}
}

func TestRelayLiveCorruptSettingsNeverFallBackToLegacy(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "relay_live.json"), []byte("broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	rm := NewRelayManager(dir)
	if err := rm.SetLiveEnabled(false); err == nil {
		t.Fatal("overwrote corrupt state")
	}
	if _, err := rm.Send(context.Background(), RelayRequest{From: "a", To: "b", SessionKey: "test:chat:user", Message: "task"}); err == nil || !strings.Contains(err.Error(), "refusing fallback") {
		t.Fatalf("unsafe fallback: %v", err)
	}
}

func TestRelayActivityRestoredSessionsAndAtomicRelease(t *testing.T) {
	file := filepath.Join(t.TempDir(), "sessions.json")
	sm := NewSessionManager(file)
	sm.GetOrCreateActive("chat")
	a := &relayActivity{}
	restored := NewSessionManager(file, a).GetOrCreateActive("chat")
	if !restored.TryLock() || a.reserve() {
		t.Fatal("restored busy session not counted")
	}
	restored.Unlock()
	if !a.reserve() || !restored.tryLock(true) {
		t.Fatal("delegated lock rejected")
	}
	restored.Unlock()
	if !restored.TryLock() {
		t.Fatal("reservation not released atomically")
	}
	restored.Unlock()
}
