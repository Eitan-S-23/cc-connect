# Release-First Program Updates

## 1. Scope / Trigger

Any cc-connect program deployment, including local Windows, Huawei and both
numbered Tencent containers. AGENTS.md is the authorization boundary; isolated
testing is not permission to install a local build.

## 2. Signatures

~~~text
.github/workflows/release-fork.yml: verify -> build[6] -> release
tag: fork-<version>; notes: docs/releases/<version>.md
go test -race -tags goolm -count=1 -timeout 10m ./...
go test -race -tags goolm -count=1 ./core -run TestCUJ
/upgrade or /update: check only
/upgrade confirm: install and request authorized restart
CC_UPDATE_ARCHIVE_DIR=<project-local-downloads>
CC_UPDATE_ARCHIVE_TAG=fork-<version>
~~~

## 3. Contracts

- Commit exact source and tag it; publish only artifacts from successful Actions
  verification/builds. An explicitly authorized CI-first release may submit a
  dedicated branch/tag for testing, but cannot merge/publish/deploy failed CI.
- The verify job builds web assets, builds/vets all Go packages and runs the full
  race suite, CUJs and a non-skipped permission regression as a non-root user.
- The six build jobs share the verified web artifact and embed the tag version,
  source commit and authorized update repository. Release assets remain immutable.
- Each archive has exactly the expected version/OS/architecture executable.
  The combined checksums.txt covers exactly six archives. Stage a draft, verify
  all seven assets, then publish; drafts do not satisfy the deployment gate.
- release-env.sh resolves every chosen cache/home/temp/build output before creating
  it. Controlled outputs stay under GITHUB_WORKSPACE; platform-maintained Actions
  command/state files are not custom evidence paths.
- Explicitly override and validate GOBIN as well as GOPATH before go install.
  An inherited GOBIN takes precedence over GOPATH/bin and can otherwise write to
  a system tool directory despite all dependency caches being project-local.
- An installed unpublished equal-semver build can lack publication metadata.
  Select a strictly higher semver/prerelease version instead of inventing that
  metadata, lexically sorting suffixes or weakening the updater.
- Publishing does not authorize installation. Use the target robot's built-in
  updater and preserve existing runtime state. Missing assets, failed CI or an
  incompatible updater require user input, never SSH/SCP/manual-copy fallback.

## 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| Controlled output escapes workspace | Fail before creating it |
| Inherited/sibling-project GOBIN | Reject before any mkdir or go install |
| Full suite, CUJ, web or permission check fails | No release build/publication |
| Tag does not identify checked-out source | Fail before packaging/publication |
| Fewer/more than six archives | Fail before draft creation |
| Draft asset count is not seven | Keep unpublished; stop |
| Release already exists | Refuse asset overwrite |
| Current unpublished beta.1; candidate beta.2 | Select candidate without current-tag lookup |
| Current unknown equal-semver metadata | Keep existing fail-closed behavior |
| Updater failure or no deployment authorization | Stop; no direct executable replacement |

## 5. Good / Base / Bad Cases

- Base: a tagged commit produces six archives and one checksum manifest.
- Good: beta.2+dir-ls.1 is discovered from unpublished beta.1+dir-ls.test.2.
- Bad: CI artifacts or a locally compiled binary are uploaded as an allegedly
  reproducible Release and deployed before the Actions gate.

## 6. Tests Required

- TestForkUpdateUnpublishedDirListBuildToHigherPrerelease asserts the selected
  tag and only one metadata request, with no current-tag endpoint required.
- The output-preflight regression supplies GOBIN=GITHUB_WORKSPACE-other/tools;
  it must fail before creating outputs, then pass with the project-local GOBIN.
- Existing unknown-current-version regressions still fail closed.
- TestForkUpdatePublishedArchiveFixtures accepts an explicit tag and checks all
  six real archive hashes, exact members and executable architectures offline.
- Full Actions verification and every matrix job must pass; check the final
  non-draft Release's source tag, six asset names/sizes and checksum manifest.
- Never relabel an older isolated package pass as a new full-repository CI pass.

## 7. Wrong vs Correct

~~~text
Wrong: publish beta.1+dir-ls.1 and assume the unpublished test suffix sorts older.
Correct: use higher prerelease precedence and retain fail-closed metadata checks.

Wrong: failed Actions -> build locally -> SCP the executable into service.
Correct: stop, report the failed gate, and request the user's next step.
~~~
