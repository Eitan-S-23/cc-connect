# Unit-Test Contracts

This is a Go repository. Use `go test`, CUJ tests, `go vet` and Go compilation;
generic `pnpm` examples do not define validation for these packages.

Read [directory-list tests](dir-list.md) for changes in `core/` or
`platform/feishu/` that affect browsing, card callbacks or directory switching.

## Quality Check

- Run focused feature and CUJ tests, followed by the full affected packages.
- Use real temporary filesystem fixtures and mock only external agent/platform
  boundaries. Every chosen output must remain in approved project-local storage.
- Prove new failure regressions fail before the production fix and pass after it.
- Report skips, platform limits, baseline failures and untested live interactions.
