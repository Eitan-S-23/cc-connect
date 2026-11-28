# Official fixes and model refresh integration

Base: custom commit `2977660a52fd6cba06bf472e778aabeff832189e` (`v1.5.1-beta.4+relay-live.1`).
Official source: https://github.com/chenhg5/cc-connect, stable tag `v1.5.4` (`ba299781cb24c8839669cc0d971aff563e06c533`).

This is a selective backport onto the custom version, not a replacement with the entire upstream release. The existing relay, autoretry, notification, directory, recovery and model registration features remain integrated.

## Behavior

- Feishu: shared connection lifecycle reporting, SDK 3.12.2, recovery from silent connections and exhausted reconnect attempts, fresh clients and bounded backoff; permanent configuration errors stop retrying.
- Codex: asynchronous questions and answer steering, child-thread event isolation, named permission profiles, configurable question timeout, correct turn completion/shutdown handling, early session-ID persistence and large rollout/resume responses.
- Sessions: generation-based busy locks, dead-process lock recovery, workspace/thread ownership, switch-history preservation, stale question handling, idle activity accounting and deferred model restarts.
- `/model refresh`: fetch the active Codex/Claude Code provider API even when a configured or native catalog exists; handle root URLs and URLs ending in `/v1`; save a successful list per project/agent/provider and hashed endpoint/auth identity. Preserve the prior list on errors, cancellation, empty results or persistence failures. Model selection and live tasks are unaffected. Refreshed lists survive reload and retain manually registered/current models.
- The refresh button uses the existing asynchronous `cmd:` dispatch. Model selection buttons carry names, so list reordering does not change their meaning. Numeric text commands remain available. Explicit Codex refresh includes gateway-specific model IDs returned by the provider; availability does not guarantee that every provider model supports the agent protocol.

## Integration changes beyond the official patches

- Retain custom `EventSessionStarted`, retry synchronization and failed-turn deduplication instead of emitting empty text startup events or losing retry behavior.
- Retain first-line cwd rejection and one-pass session-ID/path indexing in Codex list/switch scans; combine these with upstream split-rollout deduplication, larger records and error handling. A malformed first line still uses the custom full-parse fallback.
- Preserve relay activity/reservation accounting when locks are released, forcibly broken, or replaced. Stale generations cannot release a newer relay task.
- Carry lock generations through the custom retry, relay and recovery paths; use conversation keys for recovery ownership filtering.
- Apply upstream deferred model restart behavior to both custom card paths as well as the text command. Failed model changes do not close the session.
- Reacquire the interactive-state map lock when graceful cancellation fails before falling back to close.
- Skip upstream skill-only and Russian-documentation edits whose corresponding files/features do not exist in this custom base. Reuse existing custom lifecycle interfaces and test helpers where equivalent.

## Official commits

| Commit | Official change |
| --- | --- |
| [6c0683cc](https://github.com/chenhg5/cc-connect/commit/6c0683cc4d03aa75c84b585dfcd429d9f6225fc9) | fix(feishu): recover silent WebSocket connections |
| [82629446](https://github.com/chenhg5/cc-connect/commit/826294465e335cc64df146ab43da93ac0f615da4) | test(feishu): isolate recovery binary home and data directory |
| [1e02c9c8](https://github.com/chenhg5/cc-connect/commit/1e02c9c8a37cee5cd91ddf7be9aadd8c2928d64c) | test(feishu): handle recovery test listener cleanup |
| [f0f1c635](https://github.com/chenhg5/cc-connect/commit/f0f1c635196d6b338990f30d1ddbb44140ae36a9) | test(feishu): attach binary recovery documentation to its test |
| [a825ae8b](https://github.com/chenhg5/cc-connect/commit/a825ae8b16016be617727bf3a15d044a4aa82f6c) | fix(feishu): respect application log levels in SDK logger |
| [83254ad8](https://github.com/chenhg5/cc-connect/commit/83254ad865f9d46a7cee3f876195ee3b03739cb6) | fix(feishu): track connection lifecycle across shared platforms |
| [2a6877b6](https://github.com/chenhg5/cc-connect/commit/2a6877b6ac44f4fc02fd1eeb8c2f27a9c1aa3b9d) | test(feishu): check webhook server cleanup |
| [01744213](https://github.com/chenhg5/cc-connect/commit/01744213339c59ca9391d5f16474fb84f2303c9e) | fix(feishu): restart WebSocket after SDK reconnect exhaustion |
| [5894ff26](https://github.com/chenhg5/cc-connect/commit/5894ff26864983f01da0b33ac1f550ceefd131a7) | fix(codex): handle large app-server resume responses |
| [312144c2](https://github.com/chenhg5/cc-connect/commit/312144c2b5f08ba9ab58f49186eb33822bef91b9) | fix(codex): don't pass --listen for stdio app-server transport (#1786) |
| [3194660a](https://github.com/chenhg5/cc-connect/commit/3194660a8789170e288e0895d64569fd26814a9f) | fix(codex): persist session ID on thread start (#1623) |
| [bdbb3468](https://github.com/chenhg5/cc-connect/commit/bdbb3468ea3c340beb75d811fc757695ed2953a8) | fix(codex): reject concurrent exec turns (#1881) |
| [82d8d5f1](https://github.com/chenhg5/cc-connect/commit/82d8d5f1d830bf9bf330ae8c4eacf2e47573017f) | feat(codex): extend GPT-5.6 reasoning and fallback support (#1815) |
| [48c4243c](https://github.com/chenhg5/cc-connect/commit/48c4243cc3f9ac381e1d34b5b4c579d95f77950a) | fix(codex): read large session history records reliably |
| [dac61603](https://github.com/chenhg5/cc-connect/commit/dac61603623b7c65f354a6a1dc968bf2b8ad8582) | fix(codex): surface corrupt history at user entry points |
| [8f0701e6](https://github.com/chenhg5/cc-connect/commit/8f0701e642e9d81b97b8173955677acfc78511e6) | style(codex): use role switch for session summary |
| [dfad1941](https://github.com/chenhg5/cc-connect/commit/dfad19415a38b00b2c5c288610784d1a7eef337f) | fix(codex): discover current and legacy macOS desktop CLIs (#1928) |
| [44fac846](https://github.com/chenhg5/cc-connect/commit/44fac846db5eb6d4590b27150e4a56534cf9a5f6) | test(codex): cover non-UTC history timestamps |
| [ef631d7c](https://github.com/chenhg5/cc-connect/commit/ef631d7c1a3369146b774abb9230d44d0d503fc1) | fix(codex): offer GPT-6.1 Sol and GPT-6 fallback models |
| [46a41e3e](https://github.com/chenhg5/cc-connect/commit/46a41e3ebb0dd731465411870855f814faac235a) | fix(codex): prevent concurrent image attachment overwrites |
| [726f1b68](https://github.com/chenhg5/cc-connect/commit/726f1b68c29431f1ae122ce25e590b7165f6d813) | fix(codex): isolate subagent app-server events |
| [1246c2ed](https://github.com/chenhg5/cc-connect/commit/1246c2ed333c175a70079c7754405012aa4a995e) | fix(codex): reject child app-server requests before surfacing them |
| [0d4376e6](https://github.com/chenhg5/cc-connect/commit/0d4376e6747920ff425d921970e2ae792039d311) | fix(codex): deliver async questions and steer user answers |
| [9a375c35](https://github.com/chenhg5/cc-connect/commit/9a375c351d2b7c62b9dd27df2c210b7a4428ed2b) | fix: 合并 Codex 重复 rollout 会话 |
| [61018981](https://github.com/chenhg5/cc-connect/commit/61018981ee36a1e1691b550f8ef3d0b220bddc56) | fix(codex): wait for completed exec turns before follow-ups (#2063) |
| [c3e5d30e](https://github.com/chenhg5/cc-connect/commit/c3e5d30edf3d67411bea72f8f10ace4ed9b7abde) | test(codex): verify app-server arguments and resumed effort |
| [8907c2f8](https://github.com/chenhg5/cc-connect/commit/8907c2f8dd561cd32e9a911a28f2be1559c338ea) | test(codex): acknowledge fixture close error |
| [f84badac](https://github.com/chenhg5/cc-connect/commit/f84badac168a602823a63e60c64f50a5967e0b27) | fix(codex): reliably terminate turns on transport shutdown |
| [49d39e12](https://github.com/chenhg5/cc-connect/commit/49d39e12330e7ccf7acd1df133a44826926f06d6) | fix(codex): wait for turn completion instead of thread idle |
| [5671c725](https://github.com/chenhg5/cc-connect/commit/5671c725ed14bf269ae7d5607db57bb54213f406) | feat(codex): support named app-server permission profiles |
| [554ad888](https://github.com/chenhg5/cc-connect/commit/554ad88837509620724bb4231436bdab9f5b5084) | test(codex): give the 11 MB read-loop case a timeout that survives a loaded runner |
| [5078ccf3](https://github.com/chenhg5/cc-connect/commit/5078ccf300a255c4b98435bb8e38e9ce502fb7d5) | fix(codex): steer active app-server turns |
| [0cb00c2f](https://github.com/chenhg5/cc-connect/commit/0cb00c2f9b30a2c167aab6b1e5408c7afd5cbafa) | fix(codex): recover from stale steering turn IDs |
| [ea8874c4](https://github.com/chenhg5/cc-connect/commit/ea8874c49c91551f323f2f6c800b09243fec3722) | feat(codex): make app-server AskUserQuestion timeout configurable |
| [4c7ad520](https://github.com/chenhg5/cc-connect/commit/4c7ad520a38527239f1dc3495bc4f56d7ad91524) | fix(core): lock replacement session before releasing the old one in idle auto-reset (#1847) |
| [781ead2b](https://github.com/chenhg5/cc-connect/commit/781ead2b7106e910e3c2ac78ac6f514c6188dbd7) | fix(core): generation-counted session busy lock with stale-break self-heal and stop-path release (#1838) |
| [22ef60fe](https://github.com/chenhg5/cc-connect/commit/22ef60fe2e3ab0af89304f35a3e784cb5375a537) | fix(core): invalidate stale workspace sessions (#1699) (#1699) |
| [1d38af3f](https://github.com/chenhg5/cc-connect/commit/1d38af3fe327ce6321641b128cb91153df90b279) | fix(core): derive workspace session store from the engine store path (#1926) |
| [4069baf1](https://github.com/chenhg5/cc-connect/commit/4069baf1aa9f17ff2d3f5ff405d3acfea6115319) | fix(core): keep workspace bindings scoped to the platform's thread |
| [8804501d](https://github.com/chenhg5/cc-connect/commit/8804501df83739835d48f63edbd83f8d9e4c7117) | fix(core): stop clearing history on card-driven /switch |
| [961655de](https://github.com/chenhg5/cc-connect/commit/961655de14ec56707c27f9531c21d494f7b3594a) | fix(core): count queued user messages toward reset_on_idle_mins |
| [0a4b4617](https://github.com/chenhg5/cc-connect/commit/0a4b4617264d582ac58e33aee534fc15641605e5) | fix(core): drop AskUserQuestion option taps that no longer match a question |
| [cfe73f14](https://github.com/chenhg5/cc-connect/commit/cfe73f14ed99bf9296a0ec1a76b209cc73885d17) | fix(core): keep receiving background events after turn errors |
| [e2025ac1](https://github.com/chenhg5/cc-connect/commit/e2025ac18eb36f71ced389c2c730252de316d03a) | fix(core): defer /model session restart until the running turn completes |

## Deployment

Local builds are for tests only. Deployment must follow AGENTS.md: committed/tagged source → passing required CI → GitHub Actions published Release in the authorized fork → built-in updater, with administrator identity and maintenance authorization. No production binary or configuration is replaced by this worktree.
