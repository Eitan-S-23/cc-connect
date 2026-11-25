# Live Delegation Tests

Use real Engine, SessionManager and RelayManager; stub only platform transport
and agent execution. Drive setup through ReceiveMessage, emit controlled agent
events, and assert actual target output before producing the final EventResult.

- CUJ: register B inbox, enable globally in A, bind B, dispatch, observe target
  progress/tool output, finish, verify A result and B conversation continuity.
- Activity: another chat, workspace, side session, legacy relay and teardown all
  reject dispatch. Simultaneous normal lock/reservation has exactly one winner.
- Concurrent relay: one accepted request, all other attempts rejected busy.
- Permissions: B receives the normal prompt, does not auto-approve, remains busy.
- Toggle: any bot controls the same setting; off persists over config on;
  an existing delegated task still streams and finishes.
- Timeout: accepted receipt, continued execution, busy reservation retained.
- Routing: wrong source chat, missing target, changed inbox and offline target
  cannot execute; all messages route to the target's actual inbox.
- Authorization: group/unknown private metadata, missing/non-admin actors,
  allowlists and revoked roles fail closed, including card callbacks.
- Persistence failure: runtime state and links remain unchanged.
- Legacy relay tests continue to pass with default off.

CI runs `go test -race -tags goolm -count=1 -timeout 10m ./...` and explicit CUJs,
then cross builds all six existing release targets. Local execution of tests
would compile Go and is excluded by the user's Actions-only build requirement.
