# Persistent Agent Preferences

## 1. Scope / Trigger

Changes to `/mode`, `/reasoning`, their card actions, project-state storage or
workspace-agent construction. Configuration defaults and explicit runtime
selections are different sources of state.

## 2. Signatures

```go
type AgentPreferences struct {
    Mode            string `json:"mode,omitempty"`
    ReasoningEffort string `json:"reasoning_effort,omitempty"`
}
func (ps *ProjectStateStore) AgentPreferences(agentName, workspace string) AgentPreferences
func (e *Engine) restoreAgentPreferences(agent Agent, workspace string)
func (e *Engine) agentPreferenceCardContext(sessionKey string) (Agent, *SessionManager, string, error)
```

Storage is the existing `<data_dir>/projects/<sanitized-project>.state.json`:

```json
{
  "agent_preferences": {
    "example-agent": {
      "": {"mode": "default", "reasoning_effort": "high"},
      "/absolute/workspace": {"mode": "yolo", "reasoning_effort": "medium"}
    }
  }
}
```

## 3. Contracts

- The empty workspace key is the project's default agent. Other keys identify
  normalized effective workspace directories, including channel `/dir`
  overrides. Agent type is a separate namespace; settings cannot leak when a
  project changes agent type. This preserves the existing project/workspace
  scope, not a new per-human-user scope.
- Save only explicit selections, preserving unrelated project-state fields.
  Do not rewrite `config.toml`, provider credentials or CLI profile files.
  Missing preferences leave configured defaults unchanged.
- `SetProjectStateStore` restores the default agent during startup, before
  workspace snapshots or platform startup. `getOrCreateWorkspaceAgent` restores
  workspace-specific choices after construction, including idle recreation.
- Save the agent's normalized mode/effort, not a menu index or alias. Restore
  only values still advertised by that agent's capability interfaces. Legacy,
  missing and unsupported values fall back to configured/inherited defaults;
  unreadable or corrupt state is logged by `ProjectStateStore`.
- The engine serializes selector updates and persistence. The store updates
  both nested maps by copy-on-write under its mutex and uses `AtomicWriteFile`.
  A failed save rolls back the selector and stored snapshot before the caller
  can reset history, close a live session or cancel a pending retry. Report a
  localized failure without exposing raw filesystem errors to the chat.
- Text commands and `act:` cards use the same setters. Card lookup must retain
  workspace errors instead of falling back to the project agent. Both display
  and mutation use the same resolved agent. Navigation and invalid reasoning
  arguments never save a preference.
- Keep existing successful session behavior: reasoning resets its resolved
  session; mode updates a capable live session without closing it. Card mode
  fallback retains its existing session reset behavior.
- A nil store remains memory-only for embedded engines/tests. Normal CLI
  startup always attaches the project store. Persisted state must remain on
  persistent storage for host/container restarts to retain it.

## 4. Validation & Error Matrix

| Condition | Result |
| --- | --- |
| Valid command or card selection | Save canonical value; later startup restores it |
| Later selection, including the configured default | Replace the earlier selection |
| Different project, agent type or workspace | Independent preferences |
| Workspace evicted from the pool | Recreated agent restores its selections |
| Workspace creation fails during a card action | Error card; no default-agent mutation |
| Existing channel directory override | Save/render the effective workspace agent |
| Missing/old/corrupt/unsupported state | Keep configured defaults; no eager rewrite |
| Atomic save fails | Roll back settings; retain history, live session and retry |
| Live mode update succeeds | Keep the running session and its history |

## 5. Good / Base / Bad Cases

- Base: select mode and reasoning, reconstruct the store and engine, query both.
- Good: two workspaces retain different selections while project defaults,
  remembered models and notification state remain independent.
- Bad: call `sessions.Save()` and assume it serialized fields on the agent.

## 6. Tests Required

See [preference tests](../unit-test/agent-preferences.md). Keep a demonstrated
pre-fix failure for the restart and save-failure regressions. Isolated tests do
not prove real Feishu delivery or authorize upgrading/restarting any instance.

## 7. Wrong vs Correct

Wrong: `SetReasoningEffort(target)` followed only by `sessions.Save()`.

Correct: save through `setReasoningPreference`, return on failure, then perform
the existing session reset. Restore through the project store on startup and
workspace recreation; cover both commands and card callbacks.
