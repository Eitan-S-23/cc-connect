package core

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// RateLimitRetrySession opts an adapter into same-session continuation. A delay
// is a server-specified minimum. PrepareRetry must quiesce the failed producer
// before the engine drains its events and sends another prompt.
type RateLimitRetrySession interface {
	RateLimitRetryDelay(error) (time.Duration, bool)
	PrepareRetry(context.Context) error
	SetRetryProgressHandler(func(string))
}

type autoRetryRun struct {
	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	userKey  string
	threadID string
	attempts int
	next     time.Time
	delays   [3]time.Duration
}

func (e *Engine) resetAutoRetryForQueued(r *autoRetryRun) {
	if r == nil {
		return
	}
	e.autoRetryMu.Lock()
	defer e.autoRetryMu.Unlock()
	r.cancel()
	r.ctx, r.cancel = context.WithCancel(e.ctx)
	r.mu.Lock()
	r.attempts, r.next = 0, time.Time{}
	r.mu.Unlock()
}

func (e *Engine) autoRetryEnabled() bool {
	return e.projectState != nil && e.projectState.AutoRetryEnabled()
}

func (e *Engine) cancelAutoRetry(userKey string) {
	e.autoRetryMu.Lock()
	defer e.autoRetryMu.Unlock()
	for _, r := range e.autoRetryRuns {
		if userKey == "" || r.userKey == userKey {
			r.cancel()
		}
	}
}

func (e *Engine) beginAutoRetry(key string, session *Session, sessions *SessionManager) *autoRetryRun {
	ctx, cancel := context.WithCancel(e.ctx)
	r := &autoRetryRun{ctx: ctx, cancel: cancel, threadID: session.GetAgentSessionID(), delays: [3]time.Duration{0, 15 * time.Second, 60 * time.Second}}
	sessions.mu.RLock()
	for userKey, id := range sessions.activeSession {
		if id == session.ID {
			r.userKey = userKey
			break
		}
	}
	sessions.mu.RUnlock()
	e.autoRetryMu.Lock()
	defer e.autoRetryMu.Unlock()
	if e.autoRetryRuns == nil {
		e.autoRetryRuns = make(map[string]*autoRetryRun)
	}
	if old := e.autoRetryRuns[key]; old != nil {
		old.cancel()
	}
	e.autoRetryRuns[key] = r
	return r
}

func (r *autoRetryRun) progressText(text string) {
	if strings.TrimSpace(text) == "" || isSilentReply(text) {
		return
	}
	r.mu.Lock()
	r.attempts = 0
	r.mu.Unlock()
}

func (r *autoRetryRun) observe(event Event) {
	if r == nil || event.Error != nil || event.Synthetic {
		return
	}
	switch event.Type {
	case EventText:
		r.progressText(event.Content)
	case EventToolResult:
		status := strings.ToLower(strings.TrimSpace(event.ToolStatus))
		switch status {
		case "canceled", "aborted", "interrupted", "timeout", "timed_out":
			return
		}
		if (event.ToolSuccess != nil && !*event.ToolSuccess) || (event.ToolExitCode != nil && *event.ToolExitCode != 0) || status == "failed" || status == "error" || status == "cancelled" {
			return
		}
		if (event.ToolSuccess != nil && *event.ToolSuccess) || (event.ToolExitCode != nil && *event.ToolExitCode == 0) || status == "completed" || status == "success" || status == "succeeded" {
			r.progressText("tool completed")
		}
	}
}

func (e *Engine) retryStillCurrent(r *autoRetryRun, state *interactiveState, session *Session, sessions *SessionManager, as AgentSession) bool {
	if r.ctx.Err() != nil || !e.autoRetryEnabled() || r.userKey == "" || !as.Alive() {
		return false
	}
	state.mu.Lock()
	ok := !state.stopped && !state.attentionStopRequested && state.agentSession == as && len(state.pendingMessages) == 0
	state.mu.Unlock()
	if !ok || sessions.ActiveSessionID(r.userKey) != session.ID {
		return false
	}
	id := session.GetAgentSessionID()
	if id == "" || id == ContinueSession || as.CurrentSessionID() != id {
		return false
	}
	if r.threadID == "" {
		r.threadID = id
	}
	return r.threadID == id
}

func (e *Engine) waitAutoRetry(r *autoRetryRun, state *interactiveState, session *Session, sessions *SessionManager, as AgentSession, policy RateLimitRetrySession, err error) bool {
	delay, eligible := policy.RateLimitRetryDelay(err)
	if !eligible || !e.retryStillCurrent(r, state, session, sessions, as) {
		return false
	}
	r.mu.Lock()
	if r.attempts >= len(r.delays) {
		r.mu.Unlock()
		state.mu.Lock()
		p, replyCtx := state.platform, state.replyCtx
		state.mu.Unlock()
		e.send(p, replyCtx, e.i18n.T(MsgAutoRetryExhausted))
		return false
	}
	if delay < r.delays[r.attempts] {
		delay = r.delays[r.attempts]
	}
	r.next = time.Now().Add(delay)
	attempt := r.attempts + 1
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.next = time.Time{}; r.mu.Unlock() }()
	state.mu.Lock()
	p, replyCtx := state.platform, state.replyCtx
	stop := state.stopCh
	state.mu.Unlock()
	e.send(p, replyCtx, e.i18n.Tf(MsgAutoRetryScheduled, attempt, delay.Round(time.Second).String()))
	timer := time.NewTimer(delay)
	defer timer.Stop()
	selectionCheck := time.NewTicker(250 * time.Millisecond)
	defer selectionCheck.Stop()
waiting:
	for {
		select {
		case <-timer.C:
			break waiting
		case <-r.ctx.Done():
			return false
		case <-stop:
			return false
		case <-selectionCheck.C:
			if !e.retryStillCurrent(r, state, session, sessions, as) {
				return false
			}
		}
	}
	if !e.retryStillCurrent(r, state, session, sessions, as) {
		return false
	}
	// Bound adapter teardown separately; it must not leave a busy session stuck.
	ctx, cancel := context.WithTimeout(r.ctx, 30*time.Second)
	defer cancel()
	if err := policy.PrepareRetry(ctx); err != nil {
		slog.Warn("autoretry: adapter not ready", "project", e.name)
		if r.ctx.Err() == nil {
			e.send(p, replyCtx, e.i18n.T(MsgAutoRetryNotReady))
		}
		return false
	}
	if !e.retryStillCurrent(r, state, session, sessions, as) {
		return false
	}
	drainEvents(as.Events())
	return true
}

// Serialize the dispatch decision with new-input/off cancellation. Once claimed,
// this is running work; turning the switch off only suppresses later attempts.
func (e *Engine) dispatchAutoRetry(r *autoRetryRun, state *interactiveState, session *Session, sessions *SessionManager, as AgentSession) <-chan error {
	e.autoRetryMu.Lock()
	defer e.autoRetryMu.Unlock()
	if !e.retryStillCurrent(r, state, session, sessions, as) {
		return nil
	}
	r.mu.Lock()
	r.attempts++
	r.mu.Unlock()
	prompt := e.i18n.T(MsgAutoRetryPrompt)
	session.AddHistory("user", prompt)
	sessions.Save()
	next := make(chan error, 1)
	go func() { next <- as.Send(prompt, "", nil, nil) }()
	return next
}

// Keep the same session lock and sender/heartbeat identity across attempts.
// The caller, not a detached timer, remains responsible for queue draining.
func (e *Engine) processInteractiveEvents(state *interactiveState, session *Session, sessions *SessionManager, sessionKey string, msgID string, turnStart time.Time, stopTyping func(), sendDone <-chan error, replyCtx any) {
	state.mu.Lock()
	as := state.agentSession
	state.mu.Unlock()
	policy, supported := as.(RateLimitRetrySession)
	var r *autoRetryRun
	if supported {
		r = e.beginAutoRetry(sessionKey, session, sessions)
		policy.SetRetryProgressHandler(r.progressText)
		defer func() {
			policy.SetRetryProgressHandler(nil)
			r.cancel()
			e.autoRetryMu.Lock()
			if e.autoRetryRuns[sessionKey] == r {
				delete(e.autoRetryRuns, sessionKey)
			}
			e.autoRetryMu.Unlock()
		}()
	}
	for {
		err := e.processInteractiveAttempt(state, session, sessions, sessionKey, msgID, turnStart, stopTyping, sendDone, replyCtx, r)
		if r == nil || !e.waitAutoRetry(r, state, session, sessions, as, policy, err) {
			return
		}
		next := e.dispatchAutoRetry(r, state, session, sessions, as)
		if next == nil {
			return
		}
		state.mu.Lock()
		replyCtx = state.replyCtx
		state.mu.Unlock()
		sendDone, msgID, turnStart, stopTyping = next, "", time.Now(), nil
	}
}
