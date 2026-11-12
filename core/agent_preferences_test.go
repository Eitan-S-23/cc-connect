package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func preferenceTestEngine(t *testing.T, dir string) (*Engine, *stubModelModeAgent, *stubPlatformEngine) {
	t.Helper()
	p := &stubPlatformEngine{n: "plain"}
	agent := &stubModelModeAgent{mode: "default", reasoningEffort: "low"}
	e := NewEngine("preferences", agent, []Platform{p}, filepath.Join(dir, "sessions.json"), LangEnglish)
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
	t.Cleanup(e.cancel)
	return e, agent, p
}

func TestAgentPreferences_CommandsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	e, agent, p := preferenceTestEngine(t, dir)
	msg := &Message{SessionKey: "plain:user1", ReplyCtx: "ctx"}
	e.cmdMode(p, msg, []string{"yolo"})
	e.cmdReasoning(p, msg, []string{"3"})
	if agent.GetMode() != "yolo" || agent.GetReasoningEffort() != "high" {
		t.Fatal("commands did not apply the selected settings")
	}
	_, restarted, _ := preferenceTestEngine(t, dir)
	if restarted.GetMode() != "yolo" || restarted.GetReasoningEffort() != "high" {
		t.Fatalf("after restart: mode=%q reasoning=%q, want yolo/high", restarted.GetMode(), restarted.GetReasoningEffort())
	}
}

func TestAgentPreferences_CardsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	e, agent, _ := preferenceTestEngine(t, dir)
	for _, action := range []string{"act:/mode yolo", "act:/reasoning 3"} {
		if card := e.handleCardNav(action, "plain:user1"); card == nil {
			t.Fatalf("no card returned for %s", action)
		}
	}
	if agent.GetMode() != "yolo" || agent.GetReasoningEffort() != "high" {
		t.Fatal("cards did not apply the selected settings")
	}
	_, restarted, _ := preferenceTestEngine(t, dir)
	if restarted.GetMode() != "yolo" || restarted.GetReasoningEffort() != "high" {
		t.Fatalf("after restart: mode=%q reasoning=%q, want yolo/high", restarted.GetMode(), restarted.GetReasoningEffort())
	}
}

func TestAgentPreferences_SaveFailurePreservesSession(t *testing.T) {
	for _, command := range []string{"mode", "reasoning"} {
		for _, cardAction := range []bool{false, true} {
			name := command
			if cardAction {
				name += "_card"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				e, agent, p := preferenceTestEngine(t, dir)
				if err := os.Mkdir(filepath.Join(dir, "state.json"), 0o755); err != nil {
					t.Fatal(err)
				}
				key := "plain:user1"
				session := e.sessions.GetOrCreateActive(key)
				session.SetAgentSessionID("keep-session", agent.Name())
				session.AddHistory("user", "keep history")
				retry := e.beginAutoRetry("pending", session, e.sessions)
				var reply string
				target := "yolo"
				if command == "reasoning" {
					target = "high"
				}
				if cardAction {
					card := e.handleCardNav("act:/"+command+" "+target, key)
					if card == nil {
						t.Fatal("missing error card")
					}
					reply = card.RenderText()
				} else {
					e.ReceiveMessage(p, &Message{SessionKey: key, ReplyCtx: "ctx", Platform: "plain",
						UserID: "user1", Content: "/" + command + " " + target})
					reply = strings.Join(p.getSent(), "\n")
				}
				if agent.GetMode() != "default" || agent.GetReasoningEffort() != "low" {
					t.Fatal("failed save changed runtime preferences")
				}
				if session.GetAgentSessionID() != "keep-session" || len(session.GetHistory(0)) != 1 {
					t.Fatal("failed save cleared the existing session")
				}
				if retry.ctx.Err() != nil {
					t.Fatal("failed save canceled the pending retry")
				}
				if got := e.projectState.AgentPreferences(agent.Name(), ""); got != (AgentPreferences{}) {
					t.Fatalf("failed save published preferences: %+v", got)
				}
				if !strings.Contains(strings.ToLower(reply), "save") {
					t.Fatalf("missing persistence failure in reply: %s", reply)
				}
			})
		}
	}
}

func TestAgentPreferences_LastSelectionAndProjectIsolation(t *testing.T) {
	dir := t.TempDir()
	e, agent, p := preferenceTestEngine(t, dir)
	msg := &Message{SessionKey: "plain:user1", ReplyCtx: "ctx"}
	e.cmdMode(p, msg, []string{"yolo"})
	e.cmdReasoning(p, msg, []string{"high"})
	e.cmdMode(p, msg, []string{"default"})
	e.cmdReasoning(p, msg, []string{"medium"})
	if err := e.projectState.SetNotifyEnabled(false); err != nil {
		t.Fatal(err)
	}
	restarted, after, _ := preferenceTestEngine(t, dir)
	if after.GetMode() != "default" || after.GetReasoningEffort() != "medium" {
		t.Fatal("restart did not restore the last explicit selections")
	}
	if enabled, set := restarted.projectState.NotifyEnabled(); enabled || !set {
		t.Fatal("preference writes lost unrelated project state")
	}
	_, other, _ := preferenceTestEngine(t, t.TempDir())
	if other.GetMode() != "default" || other.GetReasoningEffort() != "low" {
		t.Fatal("preferences leaked into another project")
	}
	differentAgent := &namedStubModelModeAgent{name: "different-agent", stubModelModeAgent: stubModelModeAgent{
		mode: "default", reasoningEffort: "low",
	}}
	different := NewEngine("preferences", differentAgent, nil, "", LangEnglish)
	t.Cleanup(different.cancel)
	different.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
	if differentAgent.GetReasoningEffort() != "low" {
		t.Fatal("preferences leaked into a different agent type")
	}
	if got := restarted.projectState.AgentPreferences(agent.Name(), ""); got.Mode != "default" || got.ReasoningEffort != "medium" {
		t.Fatalf("persisted selections = %+v", got)
	}
}

func TestAgentPreferences_DefaultsAndInvalidState(t *testing.T) {
	for name, raw := range map[string]string{
		"missing": "",
		"legacy":  `{"auto_retry_enabled":true}`,
		"broken":  `{`,
		"retired": `{"agent_preferences":{"stub":{"":{"mode":"retired","reasoning_effort":"retired"}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if raw != "" {
				if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(raw), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			e, agent, p := preferenceTestEngine(t, dir)
			if agent.GetMode() != "default" || agent.GetReasoningEffort() != "low" {
				t.Fatal("invalid or missing preferences replaced configured defaults")
			}
			msg := &Message{SessionKey: "plain:user1", ReplyCtx: "ctx"}
			e.cmdMode(p, msg, nil)
			e.cmdReasoning(p, msg, nil)
			e.cmdReasoning(p, msg, []string{"not-supported"})
			e.handleCardNav("act:/reasoning 99", msg.SessionKey)
			if raw == "" {
				if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
					t.Fatal("read-only or invalid commands wrote a state file")
				}
			} else {
				data, err := os.ReadFile(filepath.Join(dir, "state.json"))
				if err != nil || string(data) != raw {
					t.Fatal("read-only or invalid commands rewrote the state file")
				}
			}
		})
	}
}

type preferenceWorkspaceAgent struct {
	namedStubModelModeAgent
	workDir string
}

func (a *preferenceWorkspaceAgent) GetWorkDir() string { return a.workDir }

func TestAgentPreferences_WorkspaceCommandsAndCardsSurviveRestart(t *testing.T) {
	for _, entry := range []string{"command", "card", "card_dir_override"} {
		t.Run(entry, func(t *testing.T) {
			dir := t.TempDir()
			agentName := "preferences-" + t.Name()
			RegisterAgent(agentName, func(opts map[string]any) (Agent, error) {
				mode, _ := opts["mode"].(string)
				effort, _ := opts["reasoning_effort"].(string)
				workDir, _ := opts["work_dir"].(string)
				return &preferenceWorkspaceAgent{namedStubModelModeAgent: namedStubModelModeAgent{
					name: agentName, stubModelModeAgent: stubModelModeAgent{mode: mode, reasoningEffort: effort},
				}, workDir: workDir}, nil
			})
			makeEngine := func() (*Engine, *namedStubModelModeAgent, *stubPlatformEngine) {
				p := &stubPlatformEngine{n: "feishu"}
				root := &namedStubModelModeAgent{name: agentName, stubModelModeAgent: stubModelModeAgent{
					mode: "default", reasoningEffort: "low",
				}}
				e := NewEngine("preferences", root, []Platform{p}, filepath.Join(dir, "sessions.json"), LangEnglish)
				e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
				e.SetMultiWorkspace(dir, filepath.Join(dir, "bindings.json"))
				t.Cleanup(e.cancel)
				return e, root, p
			}
			e, root, p := makeEngine()
			workspaces := []string{normalizeWorkspacePath(t.TempDir()), normalizeWorkspacePath(t.TempDir())}
			modes := []string{"yolo", "default"}
			efforts := []string{"high", "medium"}
			for i, workspace := range workspaces {
				channel := fmt.Sprintf("channel-%d", i)
				key := "feishu:" + channel + ":owner"
				boundWorkspace := workspace
				if entry == "card_dir_override" {
					boundWorkspace = normalizeWorkspacePath(t.TempDir())
					e.projectState.SetWorkspaceDirOverride(boundWorkspace+":"+key, workspace)
					e.projectState.Save()
				}
				e.workspaceBindings.Bind("project:preferences", channel, channel, boundWorkspace)
				if entry != "command" {
					e.handleCardNav("act:/mode "+modes[i], key)
					card := e.handleCardNav("act:/reasoning "+efforts[i], key)
					if card == nil || !strings.Contains(card.RenderText(), e.i18n.Tf(MsgReasoningCurrent, efforts[i])) {
						t.Fatal("workspace card did not show the selected effort")
					}
				} else {
					msg := &Message{SessionKey: key, ReplyCtx: "ctx", Platform: "feishu", UserID: "owner"}
					e.cmdMode(p, msg, []string{modes[i]})
					e.cmdReasoning(p, msg, []string{efforts[i]})
				}
			}
			if root.GetMode() != "default" || root.GetReasoningEffort() != "low" {
				t.Fatal("workspace selection changed the project defaults")
			}
			// The same restoration path serves both idle eviction and a process restart.
			e.workspacePool.mu.Lock()
			delete(e.workspacePool.states, workspaces[0])
			e.workspacePool.mu.Unlock()
			recreated, _, err := e.getOrCreateWorkspaceAgent(workspaces[0])
			if err != nil || recreated.(ReasoningEffortSwitcher).GetReasoningEffort() != "high" {
				t.Fatalf("evicted workspace lost preferences: %v", err)
			}
			restarted, _, _ := makeEngine()
			for i, workspace := range workspaces {
				after, _, err := restarted.getOrCreateWorkspaceAgent(workspace)
				if err != nil {
					t.Fatal(err)
				}
				if after.(ModeSwitcher).GetMode() != modes[i] || after.(ReasoningEffortSwitcher).GetReasoningEffort() != efforts[i] {
					t.Fatalf("workspace %d did not retain its own preferences", i)
				}
			}
		})
	}
}

func TestAgentPreferences_ConcurrentSelectionsMatchPersistedState(t *testing.T) {
	e, agent, _ := preferenceTestEngine(t, t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := e.setModePreference(agent, "plain:user", "plain:user", "yolo"); err != nil {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := e.setReasoningPreference(agent, "plain:user", "plain:user", "high"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	loaded := NewProjectStateStore(e.projectState.storePath).AgentPreferences(agent.Name(), "")
	if loaded.Mode != agent.GetMode() || loaded.ReasoningEffort != agent.GetReasoningEffort() {
		t.Fatalf("persisted preferences diverged from memory: %+v", loaded)
	}
}

type canonicalPreferenceAgent struct{ stubModelModeAgent }

func (a *canonicalPreferenceAgent) SetMode(mode string) {
	if strings.EqualFold(mode, "yolo") || strings.EqualFold(mode, "bypassPermissions") {
		a.mode = "bypassPermissions"
	} else {
		a.mode = "default"
	}
}

func (a *canonicalPreferenceAgent) PermissionModes() []PermissionModeInfo {
	return []PermissionModeInfo{{Key: "default", Name: "Default"}, {Key: "bypassPermissions", Name: "YOLO"}}
}

func TestAgentPreferences_ModeAliasPersistsCanonicalValue(t *testing.T) {
	dir := t.TempDir()
	p := &stubPlatformEngine{n: "plain"}
	agent := &canonicalPreferenceAgent{}
	e := NewEngine("preferences", agent, []Platform{p}, "", LangEnglish)
	t.Cleanup(e.cancel)
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
	e.cmdMode(p, &Message{SessionKey: "plain:user", ReplyCtx: "ctx"}, []string{"YOLO"})
	if got := e.projectState.AgentPreferences(agent.Name(), "").Mode; got != "bypassPermissions" {
		t.Fatalf("saved raw alias instead of canonical mode: %q", got)
	}
	restarted := &canonicalPreferenceAgent{}
	after := NewEngine("preferences", restarted, nil, "", LangEnglish)
	t.Cleanup(after.cancel)
	after.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
	if restarted.GetMode() != "bypassPermissions" {
		t.Fatal("canonical mixed-case mode was not restored")
	}
}

func TestAgentPreferences_ModeCardKeepsLiveSession(t *testing.T) {
	e, _, _ := preferenceTestEngine(t, t.TempDir())
	key := "plain:user"
	live := &stubLiveModeSession{}
	e.interactiveStates[key] = &interactiveState{agentSession: live}
	session := e.sessions.GetOrCreateActive(key)
	session.SetAgentSessionID("keep-session", "stub")
	session.AddHistory("user", "keep history")
	e.handleCardNav("act:/mode yolo", key)
	if len(live.modes) != 1 || live.modes[0] != "yolo" || e.interactiveStates[key] == nil {
		t.Fatal("live mode update tore down the session")
	}
	if session.GetAgentSessionID() != "keep-session" || len(session.GetHistory(0)) != 1 {
		t.Fatal("live mode update cleared history")
	}
}

func TestAgentPreferences_WorkspaceCardFailureDoesNotChangeProject(t *testing.T) {
	dir := t.TempDir()
	agentName := "preference-workspace-creation-failure"
	RegisterAgent(agentName, func(map[string]any) (Agent, error) {
		return nil, errors.New("workspace agent unavailable")
	})
	agent := &namedStubModelModeAgent{name: agentName, stubModelModeAgent: stubModelModeAgent{
		mode: "default", reasoningEffort: "low",
	}}
	e := NewEngine("preferences", agent, nil, filepath.Join(dir, "sessions.json"), LangEnglish)
	t.Cleanup(e.cancel)
	e.SetProjectStateStore(NewProjectStateStore(filepath.Join(dir, "state.json")))
	e.SetMultiWorkspace(dir, filepath.Join(dir, "bindings.json"))
	e.workspaceBindings.Bind("project:preferences", "channel", "channel", normalizeWorkspacePath(dir))
	for _, action := range []string{"act:/mode yolo", "act:/reasoning high", "nav:/mode", "nav:/reasoning"} {
		card := e.handleCardNav(action, "feishu:channel:owner")
		if card == nil || !strings.Contains(card.RenderText(), "workspace agent unavailable") {
			t.Fatalf("missing workspace error for %s", action)
		}
	}
	if agent.GetMode() != "default" || agent.GetReasoningEffort() != "low" {
		t.Fatal("failed workspace lookup changed project defaults")
	}
	if _, err := os.Stat(filepath.Join(dir, "state.json")); !os.IsNotExist(err) {
		t.Fatal("failed workspace lookup saved project preferences")
	}
}

func TestAgentPreferences_FailedUpdateRetainsPreviouslySavedValues(t *testing.T) {
	dir := t.TempDir()
	e, agent, p := preferenceTestEngine(t, dir)
	msg := &Message{SessionKey: "plain:owner", ReplyCtx: "ctx"}
	e.cmdMode(p, msg, []string{"yolo"})
	path, backup := filepath.Join(dir, "state.json"), filepath.Join(dir, "saved.json")
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	e.cmdMode(p, msg, []string{"default"})
	if agent.GetMode() != "yolo" || e.projectState.AgentPreferences(agent.Name(), "").Mode != "yolo" {
		t.Fatal("failed update lost the previous saved mode")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	e.cmdReasoning(p, msg, []string{"high"})
	_, restarted, _ := preferenceTestEngine(t, dir)
	if restarted.GetMode() != "yolo" || restarted.GetReasoningEffort() != "high" {
		t.Fatal("next successful save published stale values after a failed update")
	}
}
