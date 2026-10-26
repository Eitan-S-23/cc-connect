# Automatic Rate-Limit Continuation

## Scope

Opt-in continuation of the same Codex conversation after a terminal temporary
rate-limit error. This is a new prompt in the existing thread, not a replay of
the original API request. Both exec and app-server transports are supported.
Other adapters remain unchanged until they implement `RateLimitRetrySession`.
Nothing automatically enables, deploys or restarts the feature.

## Commands and Storage

- `/autoretry`: status card with On, Off and Refresh actions.
- `/autoretry on` / `/autoretry off`: explicit, idempotent per-project switch.
- Authenticated cards use `act:/autoretry on|off` and `nav:/autoretry`.
- Platforms without authenticated navigation use ordinary `cmd:` buttons.
- The existing project state JSON stores `auto_retry_enabled`; absent is off.
- `ProjectStateStore.AutoRetryEnabled() bool`
- `ProjectStateStore.SetAutoRetryEnabled(bool) error`

Viewing status is read-only. Mutation requires the verified caller's existing
admin authorization and obeys command-disable policy. A failed atomic save
retains the previous value. The switch covers all conversations of one bot;
retry counters and cancellation scopes are separate per conversation. Refresh
shows the active count and, during cooldown, the next eligible retry timestamp.
Turning Off cancels pending retries, not work already dispatched. The switch
survives restart; pending work and counters do not.

## Retry Contract

1. Codex owns its internal retries. `willRetry: true` is not a terminal error.
2. After a terminal temporary rate-limit error, automatically retry at most
   three times without progress: immediately, then after 15 seconds, then 60.
3. A valid server minimum exposed by the error's `RetryAfter()` method or a
   numeric `Retry-After` / `retry_after` / `try again in` message is respected;
   an exposed HTTP-date `Retry-After` is also supported.
   The adapter cannot recover HTTP headers that the Codex CLI does not expose.
4. Genuine assistant output, including a buffered completed assistant message,
   or an explicitly successful tool result resets the counter immediately.
   It does not wait for the entire turn to complete. Whitespace, `NO_REPLY`,
   synthetic notices, reasoning/status events, tool starts, unknown tool status,
   failed tools and error events do not reset it.
5. Continued progress can therefore allow more than three retries overall.
   This is intentional, not a lifetime or total-time budget.
6. A new ordinary user request cancels the old retry scope. Stop, cancel,
   session/workspace changes and Off cancel pending work. A queued user request
   gets a fresh scope without inheriting old notification recipients.
7. Only an explicit, unchanged backend thread ID and an unchanged active local
   session can be retried. An unknown ID, the "continue latest" sentinel, a
   dead agent, pending user input or cancellation prevents dispatch.
8. The original session lock is retained. There is no detached retry sender.
   Dispatch is serialized with cancellation; a claimed dispatch is running
   work, and Off does not forcibly interrupt it.
9. Before resend, the exec adapter waits for the previous Send and failed
   process to finish. Its trailing errors are drained only after the producer
   has stopped. App-server rejects stale terminal events with another turn ID.
   Adapter readiness is bounded to 30 seconds; failure stops automatic retry.

The continuation prompt explicitly retains the original scope and authorization,
uses the existing conversation, avoids completed operations, and asks the agent
to stop if done or if human input is needed. It is stored as a new history entry;
attachments and previous tool calls are not replayed. This cannot guarantee
exactly-once external effects, so existing permission boundaries still apply.

## Adapter Signatures

    RateLimitRetryDelay(error) (time.Duration, bool)
    PrepareRetry(context.Context) error
    SetRetryProgressHandler(func(string))

`retryProgress` reports buffered assistant messages separately from rendering,
so counter resets do not emit duplicate text or change final-response layout.
The engine also observes successful tool results in the event stream.

## Validation Matrix

| Condition | Result |
| --- | --- |
| Feature absent/off | Existing error behavior; no continuation |
| Temporary token/request rate limit | Same-thread retry under the policy |
| Billing/quota exhaustion, authentication, unsupported model, encrypted-history error | No automatic retry |
| Bare HTTP 429 without a rate-limit classification | No automatic retry |
| Three consecutive automatic attempts without output | Stop and explain; manual continuation remains possible |
| Real output after retry, followed by another rate limit | Counter reset; next automatic attempt is number 1 |
| New user input during cooldown | Cancel old retry and process the new request |
| Session ID changed or unavailable | Stop; never silently create a new thread |
| Off during cooldown | Cancel wait without closing the running agent session |
| Guest or missing callback actor | Reject mutation; do not borrow card-owner identity |
| Restart | Restore switch only; do not resume old retries |

## Notifications and Recovery

`/notify` remains independent. Existing error notices are not suppressed or
converted to success notices. Retry status receipts are ordinary messages.
The original heartbeat origin is retained: heartbeat errors do not mention
the user; ordinary user turns retain the existing notification policy.
No `/new`, `/recover`, provider switch or model switch is performed automatically.

## Examples and Tests

Good: attempt 2 produces genuine output, the counter becomes zero immediately,
and a later rate limit gets a fresh attempt 1. Base: disabled bots behave as
before. Bad: resetting on "reconnecting" produces an endless no-progress loop.

Wrong: launch another CLI process immediately on any error line, possibly
resuming a different thread. Correct: classify a terminal error, quiesce the
producer, revalidate identity/cancellation, then send in the locked session.

Regression coverage includes classification and server delay, live output
resets, more than three progress-making attempts, exhaustion, buffered output,
old turn events, cancellation, queued-user routing, heartbeat non-mentions,
per-bot persistence, per-session isolation, card identity, disabled commands,
atomic-save failure and translations. CUJs use real Engine/SessionManager with
controlled agent and platform boundaries; they never contact a model provider.
