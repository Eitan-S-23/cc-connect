# Remembered Model Choices

## Behavior

`/model <name>` and `/model switch <name>` remember the resolved model ID when
selection succeeds. The next `/model` response includes it in the actual choices,
not just in the current-model heading. Selecting a different model does not
remove previous choices. Numbered choices and aliases retain their existing syntax.

Text replies, inline buttons, model cards, synchronous/asynchronous card actions,
and management API model lists use the same merged catalog. Discovery entries
retain their order, aliases and descriptions. Remembered entries are appended in
selection order, followed by the current model if otherwise missing. Names are
deduplicated case-insensitively, matching existing model lookup semantics.

## Persistence Contract

The existing per-project state JSON gains an optional field:

    remembered_models: map[string][]string

A map key is a JSON-encoded array containing the agent name and active provider
name. Without an active provider it contains only the agent name. No provider URL
or credentials are included. Each project has its own state file, so catalogs are
isolated by project, agent and provider. Workspaces using the same agent/provider
share candidate names but keep their existing independent selection semantics.

This field lives outside `config.toml`. Rebuilding configured model lists from an
API or cc-switch does not erase remembered choices. Agent discovery may prefer a
native catalog over configured models; merging happens in the engine after that
discovery, so a higher-priority native catalog cannot hide manual choices.

Production startup already attaches `ProjectStateStore` to every engine. Embedded
engines without a store retain choices only for the lifetime of that engine, using
an in-memory store. Existing state files without the new field load unchanged.

## Internal Interfaces

- `Engine.availableModels(ctx, agent)` is the catalog entrypoint for consumers.
- `mergeModelCatalog(discovered, remembered, current)` does not mutate adapter slices.
- `Engine.switchModelOnAgent(agent, target, persistConfig)` resolves persistence
  through the existing selection implementation and registers the model.
- `ProjectStateStore.withRememberedModel(scope, model, apply)` writes the catalog
  before applying selection. It serializes registration and rollback under the
  store lock; its callback must not re-enter the store or make network requests.

If the catalog cannot be written, selection is not published. If the separate
selection/config write fails, registration is rolled back. A rollback-write error
is reported rather than hidden; the in-memory catalog then matches the saved
registration. A process crash between the two file writes can leave an extra
candidate, but does not erase the previous selection or conversation history.

## Validation Matrix

| Scenario | Expected |
| --- | --- |
| Select custom ID, immediately list | ID appears as a selectable numbered/dropdown option |
| Switch away, refresh discovery, restart | Remembered ID is still selectable |
| Select remembered entry by number | Resolves using the same merged list |
| API/catalog also returns the ID | One entry; discovery alias/description preserved |
| API returns no models | Remembered and current entries remain visible |
| Different project, agent or provider | No catalog leakage |
| Workspace selection | Candidate remembered; global selected model unchanged |
| Catalog write fails | Error; previous runtime/config selection unchanged |
| Selected-model config save fails | Error; previous selection and catalog restored |
| Concurrent registrations | All successful additions retained |

## Regression Tests

`TestCUJ_M1_ManualModelRemainsInListAfterSwitchAndRestart` reproduces the original
bug through `ReceiveMessage`: selection succeeds but the numbered choices omit
the custom model. It then covers switching away, reloading the state into a new
engine, and selecting the remembered model by number.

`TestModelCatalog*` covers merging, provider/agent/project isolation, persistence,
failure rollback, concurrent state writes, cards, management API and workspaces.

The root cause was treating discovery as a complete inventory even though direct
model switching accepts names outside it. A heading-only test misses this bug:
tests must inspect the actual numbered choices or `CardSelect.Options`. Appending
only the current model is also insufficient because switching away would forget it.
