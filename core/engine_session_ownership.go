package core

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

func (e *Engine) agentSessionOwner(sessions *SessionManager, sessionKey string, session *Session) AgentSessionOwner {
	owner := AgentSessionOwner{
		Project:    e.name,
		SessionKey: sessionKey,
	}
	if sessions != nil {
		owner.StorePath = sessions.StorePath()
	}
	if session != nil {
		owner.InternalSessionID = session.ID
	}
	return owner
}

func (e *Engine) claimAgentSessionOwnership(agent Agent, sessions *SessionManager, sessionKey string, session *Session, id string) error {
	if e.sessionOwnershipRegistry == nil || strings.TrimSpace(id) == "" || id == ContinueSession {
		return nil
	}
	provider, ok := agent.(SessionOwnershipScopeProvider)
	if !ok {
		return nil
	}
	scope, err := provider.SessionOwnershipScope()
	if err != nil {
		return fmt.Errorf("resolve agent session ownership scope: %w", err)
	}
	if err := e.sessionOwnershipRegistry.Claim(scope, id, e.agentSessionOwner(sessions, sessionKey, session)); err != nil {
		return err
	}
	return nil
}

func (e *Engine) persistAgentSessionID(agent Agent, sessions *SessionManager, sessionKey string, session *Session, id string, force bool) (bool, error) {
	if strings.TrimSpace(id) == "" || id == ContinueSession {
		return false, nil
	}
	if err := e.claimAgentSessionOwnership(agent, sessions, sessionKey, session, id); err != nil {
		return false, err
	}

	oldID := session.GetAgentSessionID()
	if !force && oldID != "" && oldID != ContinueSession {
		return false, nil
	}
	if oldID == id {
		return false, nil
	}

	wasEmpty := oldID == "" || oldID == ContinueSession
	if force {
		session.SetAgentSessionID(id, agent.Name())
	} else if !session.CompareAndSetAgentSessionID(id, agent.Name()) {
		return false, nil
	}
	if wasEmpty {
		pendingName := session.GetName()
		if pendingName != "" && pendingName != "session" && pendingName != "default" {
			sessions.SetSessionName(id, pendingName)
		}
	}
	sessions.Save()
	return true, nil
}

func (e *Engine) releaseAgentSessionOwnership(agent Agent, id string) error {
	if e.sessionOwnershipRegistry == nil || strings.TrimSpace(id) == "" {
		return nil
	}
	provider, ok := agent.(SessionOwnershipScopeProvider)
	if !ok {
		return nil
	}
	scope, err := provider.SessionOwnershipScope()
	if err != nil {
		return fmt.Errorf("resolve agent session ownership scope: %w", err)
	}
	return e.sessionOwnershipRegistry.Release(scope, id)
}

func (e *Engine) ownershipConflictMessage(err error) string {
	var conflict *SessionOwnershipConflictError
	if !errors.As(err, &conflict) {
		return e.i18n.T(MsgFailedToStartAgentSession)
	}
	return e.i18n.Tf(
		MsgSessionOwnershipConflict,
		conflict.ThreadID,
		conflict.Existing.Project,
		conflict.Existing.SessionKey,
	)
}

func isSessionResumeConflict(agent Agent, err error) bool {
	classifier, ok := agent.(SessionResumeConflictClassifier)
	return ok && classifier.IsSessionResumeConflict(err)
}

func (e *Engine) logOwnershipClaimFailure(context string, err error) {
	var conflict *SessionOwnershipConflictError
	if errors.As(err, &conflict) {
		slog.Warn("agent session ownership conflict",
			"context", context,
			"scope", conflict.Scope,
			"thread_id", conflict.ThreadID,
			"owner_project", conflict.Existing.Project,
			"owner_store", conflict.Existing.StorePath,
			"owner_session_key", conflict.Existing.SessionKey,
			"requested_project", conflict.Requested.Project,
			"requested_store", conflict.Requested.StorePath,
			"requested_session_key", conflict.Requested.SessionKey,
		)
		return
	}
	slog.Error("agent session ownership claim failed", "context", context, "error", err)
}
