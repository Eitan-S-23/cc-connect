# Authenticated Directory-List Mutations

## 1. Scope / Trigger

Applies to `/dir ls`, its in-place folder/paging controls and changes to the
shared directory-switch mutation path. A rendered card can outlive its agent
capabilities, directory contents or effective workspace.

## 2. Signatures

```go
type AuthenticatedCardNavigationHandler func(action, sessionKey, userID string) *Card
func (e *Engine) handleDirListCardAction(prefix string, args []string, sessionKey, userID string) *Card
func (e *Engine) dirApply(agent Agent, sessions *SessionManager, interactiveKey, sessionKey string, args []string) (errMsg, successMsg string)
```

```text
/dir ls [page]
nav:/dir ls <encoded-directory> <page>
act:/dir ls-select <encoded-directory> <encoded-child> <page>
```

## 3. Contracts

- Callback `userID` is the verified Feishu `Operator.OpenID`, not the owner
  encoded in `session_key`. The adapter checks both `allow_chat` and `allow_from`.
  A wildcard allowlist never permits an absent operator.
- The engine independently checks the operator's command policy and admin gate
  before reading directory entries. Legacy unauthenticated navigation cannot
  service this feature; unsupported platforms receive readable text instead.
- Paths are non-empty, NUL-free, absolute paths encoded as unpadded URL-safe
  base64. A selection must be a direct child of the card-bound directory.
  Do not reinterpret it as a history index or resolve it against a new workdir.
- Listing contains direct folders, hidden folders and readable directory links,
  not regular files, file links or broken links. Name order is stable; page size
  is 20; empty lists are page 1/1. Navigation order is Home, Prev, Next.
- Validate decoding, parent relation, readability, resolved agent capability and
  `os.Stat(newDir)` before canceling a pending retry. `dirApply` owns cancellation
  at its successful mutation boundary, immediately before changing the workdir.
- Failed selection preserves session history, agent session ID, directory
  history and pending retry. Successful selection cancels the old retry and
  uses existing `dirApply` reset/persistence semantics, then shows page 1.
- Paging is read-only even when a card is stale. Home means the first page of
  that card's directory; it never means `/dir reset` or navigation to a parent.

## 4. Validation & Error Matrix

| Condition | Response | State change |
| --- | --- | --- |
| Missing/blocked actor or disabled command | Denial without folder entries | None |
| Wrong chat | Adapter returns no callback card | None |
| Malformed path/page or empty session key | Usage/error card | None |
| Missing listed directory | Read-error card | None |
| Missing/file/outside/unreadable child | Original list plus error | None |
| Resolved agent lacks `WorkDirSwitcher` | Original list plus unsupported error | None, including retry |
| Target vanishes before `dirApply` validation | Existing path error | None, including retry |
| Valid selection | Selected folder's first page | Cancel retry, reset old context, persist switch |
| Valid navigation | Same directory, requested/clamped page | None |

## 5. Good / Base / Bad Cases

- Base: 41 folders produce pages of 20, 20 and 1 rows.
- Good: clicking a saved card after another workdir is selected still uses its
  absolute child; numeric names and a literal folder named `ls` are not commands.
- Bad: directory application returns an error but a preceding cancellation has
  already destroyed the user's pending continuation.

## 6. Tests Required

- `TestDirListCardAction_UnsupportedAgentPreservesPendingRetry`: error text,
  original rows, unchanged session ID/history and non-canceled retry.
- `TestDirListCardAction_UnreadableChildPreservesSessionAndRetry`: actual POSIX
  access failure, original list and unchanged state; do not count a skip as pass.
- `TestDirListCardAction_MissingListedDirectoryPreservesSessionAndRetry`:
  error card without rows, unchanged state and retry.
- Selection tests must assert successful retry cancellation as well as reset
  and absolute target persistence. Paging tests assert the opposite.
- Feishu tests cover all four controls with allowed, blocked, absent and
  wildcard actors plus wrong-chat callbacks.
- CUJ tests exercise list, page 2, Home, Next, selection and original history.

## 7. Wrong vs Correct

```go
// Wrong: a rejected directory application still cancels the old continuation.
e.cancelAutoRetry(sessionKey)
errMsg, _ := e.dirApply(agent, sessions, interactiveKey, sessionKey, args)
```

```go
// Correct: dirApply validates the capability and target before this boundary.
info, err := os.Stat(newDir)
if err != nil || !info.IsDir() {
    return e.i18n.Tf(MsgDirInvalidPath, newDir), ""
}
e.cancelAutoRetry(sessionKey)
```
