# Attention Notifications

## Scope and Trigger

An opt-in notification supplements the normal reply when a user needs to
look at the conversation. Feishu sends a newly created text message containing
a real mention of the sender, rather than just editing a streaming card.
Normal reply content, permission buttons and questions remain unchanged.

## Configuration and Signatures

The global configuration is applied to each project engine:

    [attention_notify]
    enabled = true
    on_turn_complete = true
    on_blocked = true
    on_error = true
    mention_user = true
    min_duration_secs = 0

The whole feature defaults to disabled. When enabled, all three triggers and
mentions default to true. Explicit false values survive config saves.
`content` overrides only the successful turn-completion notice.

Platform capability:

    NotifyAttention(ctx context.Context, replyCtx any, userID, userName, text string) error

`AttentionNotifyCfg` is wired from `AttentionNotifyConfig` in the CLI entrypoint.
The event-loop-local `attentionTurn` owns notification routing and deduplication.
Its internal sender is
`notifyAttentionWithMention(p, replyCtx, userID, userName, text, allowMention)`.
`allowMention` can only restrict the global `mention_user` setting; false
selects an ordinary send instead of canceling the notification.

## Heartbeat Completion Policy

`ExecuteHeartbeat` sets `Message.FromHeartbeat`; foreground, queued and
unsolicited turns snapshot that origin independently of the synthetic sender
ID. Changing the sender to a real user is not a substitute for marking origin:
it would also enable error and question mentions on automatic heartbeats.
Ordinary user turns keep all existing notification rules.

Heartbeat mention policy follows the notification kind, not model-authored
text. A successful terminal event that emits the existing "Done" completion
notice mentions the session owner when enabled. Questions/permission requests
and errors retain their ordinary unmentioned notices. "Done" means the agent
finished the current round, not independent verification of an entire task.
The notification layer does not classify the prose of a successful final reply.

The engine does not append a hidden completion protocol to heartbeat prompts.
Older conversations may still emit `[[CC_CONNECT_HEARTBEAT_COMPLETE]]`; the
streaming filter strips it for compatibility, but it never gates a mention.
Terminal failure, interruption, delivery failure and silent replies retain
their existing lifecycle semantics.

Proactive mention capability:

    ResolveAttentionRecipient(replyCtx any) (userID, userName string, err error)

Feishu resolves only user-scoped `{platform}:{chatID}:{openID}` contexts whose
chat and platform match. It never mentions the synthetic `heartbeat` sender,
chooses an arbitrary shared-chat participant, or falls back to `@all`.
Shared-chat/thread keys without an unambiguous owner skip the extra mention
and log the reason; both the ordinary result and an unmentioned completion
notice still reach the conversation.
`mention_user = false` retains the existing plain-message opt-out.

| Heartbeat outcome | Extra notification |
| --- | --- |
| Successful terminal result + nonempty, non-silent reply | One completion notice; real mention when enabled and resolvable, with no marker requirement |
| Structured question or permission request | Ordinary blocked notice when enabled; never a mention |
| Startup/send error, terminal failure, timeout, cancellation, or unexpected exit | Ordinary error notice when enabled; never a mention |
| `NO_REPLY`, empty result, or marker without a summary | None |
| Queued/subsequent ordinary user message | Existing user-message policy, with its own sender |

Good: a normal heartbeat round ends, and its Done notice mentions its owner.
Base: a silent `NO_REPLY` heartbeat stays silent; a waiting/error notice stays
unmentioned. Bad: replacing `UserID = "heartbeat"` globally, or requiring the
model to emit a magic marker before the Done notice can mention anyone.
Correct: carry origin, distinguish notification kinds, and resolve the owner
at the platform boundary without adding a second completion condition.

Regression coverage must include split streaming markers, incomplete literal
prefixes, empty terminal payloads with streamed text, normal user text,
permission then completion, failure while waiting, queued user recovery,
unresolvable recipients and existing config opt-outs. Tests must assert both
absence of an unwanted mention and presence of the ordinary notice; checking
only mention calls misses the regression where an early return drops both.
Positive completion tests must use realistic unmarked final replies, not only
fixtures containing a marker that real agents may omit.
Feishu HTTP tests assert
one newly created text message containing the owner's real `<at>` even when
there is no original user message ID. The CUJ drives `/heartbeat run`, approval
and a later manual task to verify both policies in the same conversation.

## Contracts

- Capture the original platform, reply/thread context and sender for each turn.
  A queued message gets its own snapshot, including when its sender differs.
- Attempt at most one terminal notification per turn. A duplicate permission
  request ID triggers at most one blocked notification within that turn.
  A blocked notification followed by a later terminal notification is intentional.
- Completion means the agent ended the current turn, not that an entire task
  was independently verified as successful. Heartbeats use that same
  completion-notice definition; model-authored markers do not restrict it.
- Per-turn mention suppression must not mutate the shared configuration or
  leak into a queued/subsequent user turn. Unmentioned notices use the normal
  platform send path and retain all existing trigger and duration controls.
- Failures bypass the completion-duration threshold and do not repeat raw
  provider responses, URLs, headers, tokens or request bodies. The normal error
  reply or service log supplies diagnostic details.
- Notification errors are logged and never propagate into the agent turn.
  Rate-limit waiting and sending share a 10-second context deadline.
- Permission waits continue reading agent events. Terminal errors and process
  exit release the wait; buffered progress is bounded and stale requests cannot
  hide a terminal failure behind another unanswered question.
- A recalled message retains silent-stop behavior. Service shutdown does not
  attempt new notifications after the engine context is canceled.

## Event and Error Matrix

| Input / condition | Notification |
| --- | --- |
| Successful terminal result | Completion, subject to minimum duration |
| Bare `NO_REPLY` / silent successful result | None |
| Structured question or permission request | Waiting for input |
| Agent error, including an empty/unknown error | Failure |
| Terminal result carrying an error | Failure, never completion |
| Claude `is_error` or an `error_*` result subtype | Failure |
| Failed session startup or prompt send | Startup/send failure |
| Configured event-idle timeout or maximum turn duration | Timeout |
| Process/event stream closes before a terminal result | Unexpected exit |
| Explicit interruption/cancellation | Stopped |
| Final reply cannot be delivered | Delivery failure, not success |
| Ordinary failed tool result while the agent continues | None |
| Non-terminal compaction result | None |
| Codex error explicitly marked `willRetry: true` | None; retain the active turn |
| Codex error followed by failed `turn/completed` | One terminal error |
| Idle background session closes after work already finished | None |

Classification is based on lifecycle events, not an HTTP code allowlist or
keyword scan. Future HTTP codes and non-HTTP provider/transport failures follow
the same path once the adapter reports a terminal error.

## Good, Base and Bad Cases

- Good: a long task ends, the normal answer is delivered, and the original
  sender gets one separate mention in the same conversation/thread.
- Base: with the section absent, notification behavior is unchanged.
- Bad: an API failure is wrapped in a `result` envelope. The adapter must
  preserve failure semantics instead of showing a successful completion alert.

## Verification

Regression tests cover arbitrary status codes and status-less errors, failed
starts/sends, unexpected EOF, timeouts (including while waiting for permission),
interruption, silent recall, notification failure, failed final delivery,
duplicate events, queued sender isolation and recovery after failure.
Adapter tests distinguish Claude failed results and Codex retry/cancel events
from successful completion. Feishu HTTP tests must supply a real original
message ID, including a thread session key, and assert a new text message
with the correct mention is created in the original chat, never via the reply
or card-edit APIs. They must also reject a missing open ID rather than allow
an apparently successful unmentioned alert. Core tests cover a question
followed by the eventual completion notice to the same sender.

## Wrong vs Correct

Wrong: scan text for `400` or `429`, or treat every result envelope/tool failure
as a finished task. This misses other errors and interrupts successful retries.

Correct: use terminal lifecycle events, preserve failure/retry metadata in the
adapter, and deduplicate per turn before invoking the platform capability.

## Delivery Limits

This is best-effort notification, not an external watchdog. A killed cc-connect
process, machine shutdown, unavailable network or rejected Feishu API call can
prevent delivery. Detecting those conditions needs an independently running
monitor. A valid Feishu sender open ID is required for a real mention. An
invalid or missing ID fails the notification visibly in logs instead of
silently sending an unmentioned message. Explicit `mention_user = false`
still sends an ordinary message without claiming it was a mention alert.
Feishu attention alerts are newly created text messages in the originating
chat, including for prompts inside reply threads; the alerts themselves are
not thread replies. Device notification settings still determine whether the
user hears an alert.

Source reference for Codex terminal statuses and nested error payloads:
https://developers.openai.com/codex/app-server/

Implementation and tests do not enable the option in a live user configuration
or replace the running binary. Deployment is a separate operation.
