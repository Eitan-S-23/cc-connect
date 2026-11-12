package core

import (
	"fmt"
	"log/slog"
	"maps"
	"strconv"
	"strings"
)

// AgentPreferences contains explicit selections, not defaults from config.toml.
type AgentPreferences struct {
	Mode            string `json:"mode,omitempty"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// AgentPreferences returns a copy of the selections for an agent and workspace.
// An empty workspace identifies the project's default agent.
func (ps *ProjectStateStore) AgentPreferences(agentName, workspace string) AgentPreferences {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return ps.state.AgentPreferences[agentName][workspace]
}

func (ps *ProjectStateStore) updateAgentPreferences(agentName, workspace string, update func(*AgentPreferences)) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	previous := ps.state.AgentPreferences
	byAgent := maps.Clone(previous)
	if byAgent == nil {
		byAgent = make(map[string]map[string]AgentPreferences)
	}
	byWorkspace := maps.Clone(byAgent[agentName])
	if byWorkspace == nil {
		byWorkspace = make(map[string]AgentPreferences)
	}
	preferences := byWorkspace[workspace]
	update(&preferences)
	byWorkspace[workspace] = preferences
	byAgent[agentName] = byWorkspace
	ps.state.AgentPreferences = byAgent
	if err := ps.saveLocked(); err != nil {
		ps.state.AgentPreferences = previous
		return err
	}
	return nil
}

func (e *Engine) restoreAgentPreferences(agent Agent, workspace string) {
	if e.projectState == nil || agent == nil {
		return
	}
	preferences := e.projectState.AgentPreferences(agent.Name(), workspace)
	if switcher, ok := agent.(ModeSwitcher); ok && preferences.Mode != "" {
		for _, mode := range switcher.PermissionModes() {
			if strings.EqualFold(mode.Key, preferences.Mode) {
				switcher.SetMode(mode.Key)
				break
			}
		}
	}
	if switcher, ok := agent.(ReasoningEffortSwitcher); ok && preferences.ReasoningEffort != "" {
		for _, effort := range switcher.AvailableReasoningEfforts() {
			if strings.EqualFold(effort, preferences.ReasoningEffort) {
				switcher.SetReasoningEffort(effort)
				break
			}
		}
	}
}

func (e *Engine) saveAgentPreference(agent Agent, interactiveKey, sessionKey string, update func(*AgentPreferences)) error {
	if e.projectState == nil {
		return nil
	}
	workspace := ""
	if agent != e.agent {
		workspace = workspaceModelOverrideKey(interactiveKey, sessionKey, agent)
		if workspace == "" {
			return fmt.Errorf("cannot resolve workspace for agent preferences")
		}
	}
	return e.projectState.updateAgentPreferences(agent.Name(), workspace, update)
}

func (e *Engine) setModePreference(agent Agent, interactiveKey, sessionKey, target string) (string, error) {
	e.agentPreferencesMu.Lock()
	defer e.agentPreferencesMu.Unlock()
	switcher := agent.(ModeSwitcher)
	previous := switcher.GetMode()
	// Agents own aliases and canonical spellings, e.g. yolo -> bypassPermissions.
	switcher.SetMode(strings.ToLower(strings.TrimSpace(target)))
	mode := switcher.GetMode()
	if err := e.saveAgentPreference(agent, interactiveKey, sessionKey, func(p *AgentPreferences) {
		p.Mode = mode
	}); err != nil {
		switcher.SetMode(previous)
		slog.Error("failed to save permission mode", "project", e.name, "error", err)
		return "", err
	}
	return mode, nil
}

func (e *Engine) setReasoningPreference(agent Agent, interactiveKey, sessionKey, target string) (string, error) {
	e.agentPreferencesMu.Lock()
	defer e.agentPreferencesMu.Unlock()
	switcher := agent.(ReasoningEffortSwitcher)
	previous := switcher.GetReasoningEffort()
	switcher.SetReasoningEffort(target)
	effort := switcher.GetReasoningEffort()
	if err := e.saveAgentPreference(agent, interactiveKey, sessionKey, func(p *AgentPreferences) {
		p.ReasoningEffort = effort
	}); err != nil {
		switcher.SetReasoningEffort(previous)
		slog.Error("failed to save reasoning effort", "project", e.name, "error", err)
		return "", err
	}
	return effort, nil
}

func resolveReasoningTarget(switcher ReasoningEffortSwitcher, input string) (string, bool) {
	efforts := switcher.AvailableReasoningEfforts()
	target := strings.ToLower(strings.TrimSpace(input))
	if idx, err := strconv.Atoi(target); err == nil && idx >= 1 && idx <= len(efforts) {
		target = efforts[idx-1]
	}
	for _, effort := range efforts {
		if strings.EqualFold(effort, target) {
			return effort, true
		}
	}
	return "", false
}

// Unlike sessionContextForKey, preference edits must not fall back to the
// project agent when creating a bound workspace agent fails.
func (e *Engine) agentPreferenceCardContext(sessionKey string) (Agent, *SessionManager, string, error) {
	if workspace := e.sendWorkDirForSession(sessionKey); workspace != "" {
		agent, sessions, err := e.getOrCreateWorkspaceAgent(workspace)
		return agent, sessions, workspace + ":" + sessionKey, err
	}
	if e.multiWorkspace && e.workspaceBindings != nil {
		workspace := ""
		if binding, _ := e.workspaceBindings.LookupEffective("project:"+e.name, extractWorkspaceChannelKey(sessionKey)); binding != nil {
			workspace = normalizeWorkspacePath(binding.Workspace)
		} else {
			workspace = e.workspaceFromLiveState(sessionKey)
		}
		if workspace != "" {
			agent, sessions, interactiveKey, _, err := e.workspaceContext(workspace, sessionKey)
			return agent, sessions, interactiveKey, err
		}
	}
	return e.agent, e.sessions, sessionKey, nil
}

func (e *Engine) handleAgentPreferenceCardAction(cmd, args, sessionKey string) *Card {
	agent, sessions, interactiveKey, err := e.agentPreferenceCardContext(sessionKey)
	if err != nil {
		title := MsgCardTitleMode
		if cmd == "/reasoning" {
			title = MsgCardTitleReasoning
		}
		return e.simpleCard(e.i18n.T(title), "red", e.i18n.Tf(MsgWsResolutionError, err))
	}
	if cmd == "/reasoning" {
		switcher, ok := agent.(ReasoningEffortSwitcher)
		if !ok || args == "" {
			return e.renderReasoningCard(agent)
		}
		target, valid := resolveReasoningTarget(switcher, args)
		if !valid {
			return e.renderReasoningCard(agent)
		}
		if _, err := e.setReasoningPreference(agent, interactiveKey, sessionKey, target); err != nil {
			return e.simpleCard(e.i18n.T(MsgCardTitleReasoning), "red", e.i18n.T(MsgAgentPreferencesSaveFailed))
		}
		e.cancelAutoRetry(sessionKey)
		e.cleanupInteractiveState(interactiveKey)
		s := sessions.GetOrCreateActive(sessionKey)
		s.SetAgentSessionID("", "")
		s.ClearHistory()
		sessions.Save()
		return e.renderReasoningCard(agent)
	}

	if _, ok := agent.(ModeSwitcher); !ok || args == "" {
		return e.renderModeCard(agent)
	}
	mode, err := e.setModePreference(agent, interactiveKey, sessionKey, args)
	if err != nil {
		return e.simpleCard(e.i18n.T(MsgCardTitleMode), "red", e.i18n.T(MsgAgentPreferencesSaveFailed))
	}
	e.cancelAutoRetry(sessionKey)
	if !e.applyLiveModeChange(sessionKey, mode) {
		e.cleanupInteractiveState(interactiveKey)
		s := sessions.GetOrCreateActive(sessionKey)
		s.SetAgentSessionID("", "")
		s.ClearHistory()
		sessions.Save()
	}
	return e.renderModeCard(agent)
}
