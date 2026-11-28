package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type relayInbox struct {
	Platform   string `json:"platform"`
	SessionKey string `json:"session_key"`
	UserID     string `json:"user_id"`
}

type relayLiveLink struct {
	Source relayInbox `json:"source"`
	Target relayInbox `json:"target"`
}

type relayLiveState struct {
	Enabled *bool                               `json:"enabled,omitempty"`
	Inboxes map[string]relayInbox               `json:"inboxes"`
	Links   map[string]map[string]relayLiveLink `json:"links"`
}

func relaySourceKey(project, sessionKey string) string { return project + "\n" + sessionKey }

func (rm *RelayManager) loadLive() {
	rm.live = relayLiveState{Inboxes: make(map[string]relayInbox), Links: make(map[string]map[string]relayLiveLink)}
	if rm.livePath == "" {
		return
	}
	data, err := os.ReadFile(rm.livePath)
	if os.IsNotExist(err) {
		return
	}
	if err == nil {
		err = json.Unmarshal(data, &rm.live)
	}
	if err != nil {
		rm.liveError = err
		slog.Error("relay: cannot load live settings; refusing changes", "error", err)
	}
}

func (rm *RelayManager) SetLiveDefault(enabled bool) {
	rm.mu.Lock()
	rm.liveDefault = enabled
	rm.mu.Unlock()
}

func (rm *RelayManager) LiveEnabled() bool {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	if rm.liveError != nil {
		return false
	}
	if rm.live.Enabled != nil {
		return *rm.live.Enabled
	}
	return rm.liveDefault
}

// Copy, persist, then publish: a failed write cannot change the runtime switch
// or leave a half-updated private route. The legacy binding file is untouched.
func (rm *RelayManager) updateLive(change func(*relayLiveState) error) error {
	rm.mu.Lock()
	defer rm.mu.Unlock()
	if rm.liveError != nil {
		return rm.liveError
	}
	data, err := json.Marshal(rm.live)
	if err != nil {
		return err
	}
	var next relayLiveState
	if err := json.Unmarshal(data, &next); err != nil {
		return err
	}
	if next.Inboxes == nil {
		next.Inboxes = make(map[string]relayInbox)
	}
	if next.Links == nil {
		next.Links = make(map[string]map[string]relayLiveLink)
	}
	if err := change(&next); err != nil {
		return err
	}
	if rm.livePath != "" {
		data, err = json.MarshalIndent(next, "", "  ")
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(rm.livePath), 0o700); err != nil {
			return err
		}
		if err := AtomicWriteFile(rm.livePath, data, 0o600); err != nil {
			return err
		}
	}
	rm.live = next
	return nil
}

func (rm *RelayManager) SetLiveEnabled(enabled bool) error {
	return rm.updateLive(func(s *relayLiveState) error { s.Enabled = &enabled; return nil })
}

func (rm *RelayManager) registerInbox(project string, inbox relayInbox) error {
	return rm.updateLive(func(s *relayLiveState) error {
		if old, ok := s.Inboxes[project]; ok && old != inbox {
			// Moving B's inbox requires A to bind again; never silently redirect
			// already authorized work into a different person's conversation.
			for _, links := range s.Links {
				delete(links, project)
			}
		}
		s.Inboxes[project] = inbox
		return nil
	})
}

func (rm *RelayManager) bindLive(project string, source relayInbox, target string) error {
	return rm.updateLive(func(s *relayLiveState) error {
		inbox, ok := s.Inboxes[target]
		if !ok {
			return fmt.Errorf("target inbox missing")
		}
		key := relaySourceKey(project, source.SessionKey)
		if s.Links[key] == nil {
			s.Links[key] = make(map[string]relayLiveLink)
		}
		s.Links[key][target] = relayLiveLink{Source: source, Target: inbox}
		return nil
	})
}

func (rm *RelayManager) liveTargets(project, sessionKey string) []string {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	var names []string
	for name := range rm.live.Links[relaySourceKey(project, sessionKey)] {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (rm *RelayManager) sendLive(ctx context.Context, req RelayRequest) (*RelayResponse, error) {
	rm.mu.RLock()
	source, target := rm.engines[req.From], rm.engines[req.To]
	link, bound := rm.live.Links[relaySourceKey(req.From, req.SessionKey)][req.To]
	inbox, registered := rm.live.Inboxes[req.To]
	rm.mu.RUnlock()
	if source == nil {
		return nil, fmt.Errorf("relay: source project is unavailable")
	}
	if !bound || !registered || inbox != link.Target || req.From == req.To {
		return nil, fmt.Errorf("%s", source.i18n.Tf(MsgRelayLiveNotBound, strings.Join(rm.liveTargets(req.From, req.SessionKey), ", ")))
	}
	if target == nil || target.ctx.Err() != nil {
		return nil, fmt.Errorf("%s", source.i18n.T(MsgRelayNotAvailable))
	}
	if link.Source.UserID == "" || link.Target.UserID == "" ||
		!source.isAdmin(link.Source.UserID) || !target.isAdmin(link.Target.UserID) ||
		source.relayCommandDisabled("bind", link.Source.UserID) || target.relayCommandDisabled("bind", link.Target.UserID) {
		return nil, fmt.Errorf("%s", source.i18n.Tf(MsgAdminRequired, "/bind"))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result, err := target.startLiveRelay(req.From, req.Message, link.Target)
	if err != nil {
		return nil, err
	}
	waitCtx, cancel := rm.relayContext(ctx)
	defer cancel()
	select {
	case outcome := <-result:
		return &RelayResponse{Response: outcome.text}, outcome.err
	case <-waitCtx.Done():
		// Waiting is separate from execution. B retains the normal conversation
		// and keeps displaying progress until its turn completes or is stopped.
		return &RelayResponse{Response: source.i18n.Tf(MsgRelayLiveWaiting, req.To)}, nil
	}
}

type relayOutcome struct {
	text string
	err  error
}

func (e *Engine) startLiveRelay(from, prompt string, inbox relayInbox) (<-chan relayOutcome, error) {
	if !e.relayActivity.reserve() {
		return nil, fmt.Errorf("%s", e.i18n.T(MsgRelayLiveBusy))
	}
	reservationOwned := true
	defer func() {
		if reservationOwned {
			e.relayActivity.release()
		}
	}()
	e.closingMu.Lock()
	unsafe := len(e.closingSessions) != 0 || len(e.unsafeResume) != 0
	e.closingMu.Unlock()
	if unsafe {
		return nil, fmt.Errorf("%s", e.i18n.T(MsgRelayLiveBusy))
	}
	p := e.platformForName(inbox.Platform)
	rc, ok := p.(ReplyContextReconstructor)
	if !ok {
		return nil, fmt.Errorf("%s", e.i18n.T(MsgRelayNotAvailable))
	}
	e.platformLifecycleMu.Lock()
	ready := e.platformReady[p] && !e.stopping
	e.platformLifecycleMu.Unlock()
	if !ready {
		return nil, fmt.Errorf("%s", e.i18n.T(MsgRelayNotAvailable))
	}
	replyCtx, err := rc.ReconstructReplyCtx(inbox.SessionKey)
	if err != nil {
		return nil, fmt.Errorf("relay: reconstruct target inbox: %w", err)
	}
	msg := &Message{SessionKey: inbox.SessionKey, Platform: inbox.Platform,
		UserID: inbox.UserID, UserName: from, Content: prompt, ReplyCtx: replyCtx}
	if e.multiWorkspace {
		workspace, _, resolveErr := e.resolveWorkspace(p, effectiveChannelID(msg))
		if resolveErr != nil {
			return nil, resolveErr
		}
		if workspace == "" {
			return nil, fmt.Errorf("relay: target chat has no workspace binding")
		}
	}
	agent, sessions, key, workspace, err := e.commandContextWithWorkspace(p, msg)
	if err != nil {
		return nil, err
	}
	if forced := e.sendWorkDirForSession(inbox.SessionKey); forced != "" {
		agent, sessions, err = e.getOrCreateWorkspaceAgent(forced)
		if err != nil {
			return nil, err
		}
		workspace, key = forced, forced+":"+inbox.SessionKey
	}
	// Keep only the project reservation until delivery succeeds. A busy session
	// would let user messages queue before a processor exists to drain them.
	sendCtx, cancel := context.WithTimeout(e.ctx, 15*time.Second)
	err = p.Send(sendCtx, replyCtx, e.i18n.Tf(MsgRelayLiveReceived, from, truncateRelay(prompt, 1500)))
	cancel()
	if err != nil {
		return nil, fmt.Errorf("relay: target delivery failed: %w", err)
	}
	session := sessions.GetOrCreateActive(inbox.SessionKey)
	lockGen, locked := session.tryLock(true)
	if !locked {
		return nil, fmt.Errorf("%s", e.i18n.T(MsgRelayLiveBusy))
	}
	// The session now owns the reservation. Unlock releases both atomically,
	// avoiding a gap that could strand a normal message in an orphaned queue.
	reservationOwned = false
	done := make(chan relayOutcome, 1)
	var once sync.Once
	msg.onRelayResult = func(text string, err error) { once.Do(func() { done <- relayOutcome{text, err} }) }
	e.ensureInteractiveStateForQueueing(key, p, replyCtx)
	go func() {
		e.processInteractiveMessageWith(p, msg, session, agent, sessions, key, workspace, inbox.SessionKey, lockGen)
	}()
	return done, nil
}

func (s *interactiveState) completeRelay(text string, err error) {
	s.mu.Lock()
	callback := s.onRelayResult
	s.onRelayResult = nil
	if callback != nil && err == nil && s.attentionStopRequested {
		err = context.Canceled
	}
	s.mu.Unlock()
	if callback != nil {
		callback(text, err)
	}
}
