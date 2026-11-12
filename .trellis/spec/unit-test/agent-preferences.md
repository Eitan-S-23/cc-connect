# Agent-Preference Regression Tests

## Storage And Engine

Use a real project-state file and session manager under `t.TempDir()`, with
stub agent/platform boundaries. Reconstruct a new store and engine rather than
asserting only the original in-memory selector.

- `TestAgentPreferences_CommandsSurviveRestart` and
  `TestAgentPreferences_CardsSurviveRestart`: select both settings, reconstruct
  the engine, and assert restored values. These failed before the fix.
- `TestAgentPreferences_SaveFailurePreservesSession`: use a directory as the
  destination file to force cross-platform write failure. Text and card paths
  retain selector values, session ID/history, retry and the store snapshot;
  both expose the localized save failure. These failed before the fix.
- `TestAgentPreferences_LastSelectionAndProjectIsolation`: last choice wins,
  choosing defaults is persisted, and projects, agent types and unrelated
  notification state do not interfere.
- `TestAgentPreferences_WorkspaceCommandsAndCardsSurviveRestart`: independent
  workspace settings through commands, cards and channel directory overrides;
  verify idle recreation and process restart as separate reconstruction paths.
- `TestAgentPreferences_WorkspaceCardFailureDoesNotChangeProject`: failed
  workspace construction produces an error instead of changing project defaults.
- `TestAgentPreferences_DefaultsAndInvalidState`: absent, legacy, corrupt and
  unsupported state retain defaults; queries/invalid reasoning never rewrite it.
- Cover canonical mixed-case aliases, concurrent selectors matching the disk
  snapshot, live mode updates retaining history, and failed updates retaining
  previously saved values through the next successful write.

## User Journey

`TestCUJ_C6_ModeSwitchAcknowledged/mode_and_reasoning_survive_restart` drives
`ReceiveMessage`: select mode, select reasoning, create a new session, query,
reconstruct the engine, and query both again. Assert user-visible replies.

Run focused preference tests, all CUJs, the full affected packages, `go vet` and
compilation with all cache/home/temp outputs inside the authorized project.
Report platform skips, unrelated baseline failures and live-test gaps separately.
