# Directory List Contracts

## Scope

`/dir ls` browses actual direct subfolders separately from `/dir` history.
No parent-directory, previous-directory, reset, or help-menu controls are added.
The original history command, indices, reset and compatibility alias stay intact.

## Signatures

```text
/dir ls [page]
/dir ./ls

readDirListPage(dir string, page int) (dirListPage, error)
handleDirListCardAction(prefix string, args []string, sessionKey, userID string) *Card
```

The second command selects a literal folder named `ls`. `/cd ls` uses the same
list mode as `/dir ls`; `/cd ./ls` remains an explicit directory switch.

## Contracts

- Read `WorkDirSwitcher.GetWorkDir()` through the existing command/workspace context.
- List all direct folders, including hidden folders and symlinks to directories.
  Exclude ordinary files, broken links, file links and recursive descendants.
- Keep name ordering from `os.ReadDir`, with 20 rows per page. An empty list has
  page 1/1. Clamp callback pages to the valid range; invalid text pages show usage.
- One equal-width row contains Home, Prev, Next in that order. Boundary buttons
  keep the same page. Home always refers to the card's listed directory, page 1.
- Callback paths are URL-safe, unpadded base64 encodings of absolute paths:

```text
nav:/dir ls <encoded-directory> <page>
act:/dir ls-select <encoded-directory> <encoded-child> <page>
```

- The encoded directory remains bound to the card, even if the effective working
  directory or history ordering subsequently changes. A selected target must be
  an immediate child of that directory and readable before switching.
- Plain-text commands use the existing `/dir` administrator and role checks.
  Button actions check the verified operator, never the owner in `session_key`.
  Feishu also applies its `allow_from` filter to navigation and selection.
  Platforms without authenticated card navigation receive a readable text list.
- Paging does not modify the working directory, session history, agent session
  ID, directory history or pending auto-retry. A successful selection uses
  `dirApply`, preserving its existing session reset, history and override semantics,
  then displays the selected directory's page 1 in the original card.

## Validation and Errors

| Input or state | Result | Mutation |
| --- | --- | --- |
| `/dir ls`, `/dir ls 2` | Corresponding folder page | None |
| Page 0, negative, non-integer, extra arguments | Usage message | None |
| Callback page past last page | Last page | None |
| Empty directory | Empty-list note, page 1/1 and navigation | None |
| Missing/unreadable listed directory | Read-error card, no help/reset controls | None |
| Child removed, not a directory, or unreadable | Keep original list with error | None |
| Relative/malformed encoded path or outside child | Usage/error, no switch | None |
| Guest, missing verified operator or disabled `/dir` | Denied, no directory entries | None |
| Resolved agent no longer supports directory switching | Keep original list with unsupported error | None, including pending retry |
| Valid child selection | New folder page 1 and switch confirmation | Existing `/dir` reset semantics |

## Cases

- Base: a directory with 41 folders shows 20, 20 and 1 rows across three pages.
- Good: a folder named `2`, `ls`, with spaces or non-ASCII characters resolves
  from its encoded absolute path, not a history index or a newly changed base.
- Bad: treating Home as `/dir reset`, using a history index for a live folder,
  or accepting an owner's identity from a shared card instead of its operator.

## Required Tests

- Listing/filtering, ordering, hidden folders, directory/file/broken links.
- 0, 1, 20, 21 and 41 folders, all page boundaries and fixed navigation order.
- Exact path round-trip, special names, malformed callbacks and stale cards.
- Paging preserves history, agent session ID, directory history and retry context.
- Selection preserves existing reset/history behavior; failed selection is inert.
- Successful selection cancels the old directory's pending retry; validation and
  application errors preserve it, including a stale card with an unsupported agent.
- Verified actor, disabled command and role restrictions apply to every button.
- Feishu allows only verified `allow_from` operators through to the handler.
- CUJ: list, go to page 2, Home, Next, select, then verify original `/dir` history.
- Feishu serialization: three centered, equally weighted fill-width buttons in a
  non-wrapping column set; existing two-column bisect and ordinary rows unchanged.

## Wrong vs Correct

```go
// Wrong: relative name can switch somewhere else after a stale card is clicked.
applyArgs := []string{folderName}

// Correct: decode and validate the card-bound absolute child before dirApply.
target, err := decodeDirListPath(encodedChild)
```

Go build/test outputs, temporary files, dependency caches and deployment evidence
must use a task-specific directory under this source repository's `.cache/`.
Test releases do not replace any local npm, CLI or daemon binary.

Detailed failure-atomicity and target-platform testing contracts are maintained
in [backend specs](../.trellis/spec/backend/index.md) and
[test specs](../.trellis/spec/unit-test/dir-list.md).
