# Directory-List Test Scope

## Unit and Filesystem Tests

- Folder-only filtering, ordering, hidden folders, directory/file/broken links.
- 0/1/20/21/41 entries; first/last-page clamps; fixed Home/Prev/Next order.
- Absolute path round-trip, special/numeric names and literal `./ls` selection.
- Malformed callbacks, missing base/child, outside child, unreadable child and
  an agent that no longer has the work-directory capability.
- For failure: original/error card as appropriate, unchanged workdir/session
  ID/history/directory history and a live pending retry.
- For success: absolute target, session reset, history persistence, canceled old
  retry and selected directory page 1. Paging asserts no corresponding mutation.

## Adapter Regression

For Home, Prev, Next and selection, test verified allowlisted actor, blocked
actor, empty/missing operator, wildcard allowlist with verified/missing actor,
and a chat excluded by `allow_chat`. Reject callbacks before invoking the core
handler. Allowed callbacks return a raw in-place card update.

Inspect Feishu serialization for three equally weighted, centered, fill-width
buttons in one non-wrapping column set. Keep two-column bisect and ordinary
action-row regression tests.

## CUJ / Integration

Use a real `Engine` and `SessionManager`, driven through `ReceiveMessage`, with
mock platform sending and agent execution. At least three user actions must
assert user-visible output. Browse, page 2, Home, Next, select and return to the
original `/dir` history UI.

Linux execution is mandatory for symlink/permission coverage. Run the unreadable
child test without DAC override capabilities and reject a skipped result.

`TestDirListCardAction_RealFilesystemSmoke` takes an explicitly read-only
`CC_DIR_LIST_SMOKE_DIR` containing a `cc-connect` child. It checks the real
directory's complete paginated listing and absolute selection using an isolated
mock agent. Session/history output stays in `t.TempDir()`; no live agent is
started and the external input directory is never modified.

## Explicit Gaps

Mock callbacks and CUJs do not prove `card.action.trigger` subscription,
published App settings, real Feishu rendering or genuine button-click delivery.
Those require separately authorized live testing; never mark them done from
automatic regression or program version checks.
