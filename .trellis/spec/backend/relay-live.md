# Live Delegation

## Scope and Interfaces

- `RelayManager.Send(ctx, RelayRequest)` snapshots live/legacy mode at dispatch.
- `/relay [on|off]` changes the shared manager's persistent instance-wide setting.
- `/bind here` registers a verified private inbox; `/bind <project>` in live mode
  pins the source project/session to that registered target inbox.
- `PrivateChatDetector.IsPrivateChat(replyCtx)` uses trusted platform metadata.
- `Session.TryLock`, `Session.tryLock(true)` and `relayActivity.reserve` share
  admission accounting across normal, workspace and side session managers.

## Contracts

Default off preserves legacy bindings and request-response execution. Runtime
settings live in `relay_live.json` in the configured data directory, separate
from `relay_bindings.json`. Copy/persist/publish prevents a failed write from
changing runtime state. A saved explicit off overrides a config default on.

Both inbox registration and private binding require authenticated administrators.
Feishu open IDs differ across applications: verify the actor independently in
each bot; do not equate open IDs between apps. Card clicks use the clicker's
identity, never the card owner's. Changing an inbox invalidates incoming links.

Live dispatch reserves the entire target project only when no activity exists.
Normal session locks and old relay calls participate even while the switch is
off, so turning it on cannot hide existing work. Teardown holds activity until
its completion; unsafe process state rejects admission. Rejection never queues
or cancels. Accepted tasks use the target's normal output path and permission
policy, not legacy auto-approval. A final-turn callback returns only the
delegated result, before any later queued user turn changes routing.

The relay caller's timeout stops waiting, not execution. B keeps its reservation
and output path. Mode changes do not cancel or reroute an accepted task.
External CLI processes are outside the instance's activity accounting.

## Failure Matrix

| Condition | Outcome |
| --- | --- |
| Unknown/group chat registration | Reject |
| Missing inbox, binding, source or target | Reject without running |
| Non-admin/disabled binding | Reject |
| B busy, permission pending, stopping | Reject without queueing |
| Two concurrent dispatches | Exactly one may reserve |
| Target send fails before start | Release reservation, do not run |
| Setting persistence fails | Previous runtime setting remains |
| Waiting expires | Accepted/still-running receipt; B continues |
| Switch off during execution | Existing target output continues |
| B changes inbox | A must bind again |

## Validation

Run feature tests, CUJs, full race suite, build/vet and cross compilation only
in GitHub Actions for this task. Feature branches upload review artifacts;
publication remains restricted to the existing release-tag gate. No local or
remote runtime deployment is authorized by a successful feature branch build.
