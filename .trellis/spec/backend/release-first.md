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
whoami
GIT_SSH_COMMAND=<non-interactive SSH command with pinned hosts and read-only key>
git push --dry-run --atomic <explicit-authorized-SSH-URL> <branch-refspec> <tag-refspec>
git push --atomic <same-SSH-URL> <same-branch-refspec> <same-tag-refspec>
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
- RUNNER_* variables are platform-owned and cannot reliably redirect hosted
  caches. Install checksum-pinned Go/Node archives under PROJECT_TOOL_CACHE,
  validate archive paths/link targets before extraction, and leave platform
  tool caches read-only. GH_CACHE_DIR must be explicit before reading run logs.
- On Windows, distinguish the process identity (`whoami`) from inherited
  USERPROFILE/HOME paths. SYSTEM privileges do not automatically provide another
  user's OAuth/keyring credentials. Missing credentials are not proof that the
  token expired or that SYSTEM cannot publish. A SYSTEM-only test must not log
  in to the desktop, impersonate another user or borrow that user's SSH agent.
- An existing authorized SSH key may work under SYSTEM even when OAuth lookup
  fails. Match its public key to the expected GitHub account, keep the private
  key read-only in its approved input location, and pin host keys from GitHub's
  HTTPS metadata in a project-local known_hosts file. Never print/copy a secret
  into logs, disable host verification or alter the original credential files.
- Git for Windows OpenSSH accepts `-F /dev/null`, not the Windows `NUL` spelling.
  Use BatchMode=yes, IdentitiesOnly=yes, IdentityAgent=none,
  StrictHostKeyChecking=yes, UpdateHostKeys=no and an explicit project-local
  UserKnownHostsFile. Disable password/keyboard-interactive authentication and
  agent forwarding. All selected cache, temporary and evidence paths stay local.
- Use the explicit SSH URL of the same authorized repository for the dry run
  and push. Do not rely on `git -c remote.origin.url=...` to replace a multi-valued
  remote URL: the original HTTPS URL can still be used. Keep long-term remotes
  unchanged. SSH authentication changes only source transport, not the release
  channel, required Actions checks or updater-only deployment gate.
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
| Unsafe tool archive path/link or wrong checksum | Reject before extraction |
| SYSTEM with inherited USERPROFILE for another user | Diagnose identity and credential scope separately |
| CredReadW returns ERROR_NOT_FOUND (1168) | No matching credential in this context; do not infer token expiry |
| GitHub SSH reports the expected account with exit 1 | Authentication succeeded; GitHub does not provide shell access |
| SSH authentication succeeds but push dry run fails | Do not claim repository write access; inspect the specific failure |
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
- Good: beta.2+dir-ls.2 is discovered from unpublished beta.1+dir-ls.test.2.
- Good: SYSTEM uses an existing authorized SSH key to push to the same repository
  without a desktop login, while all publication still occurs through Actions.
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
- For a SYSTEM-only release, record process identity, the expected SSH account
  greeting, successful push and matching remote tag/commit. Verify original
  credential/key files remain unchanged and no user impersonation was used.
- Compare the six hashes in the exact Actions release-job log with published
  asset digests and downloaded files, then run the offline archive fixture test.
- Never relabel an older isolated package pass as a new full-repository CI pass.

## 7. Wrong vs Correct

~~~text
Wrong: publish beta.1+dir-ls.1 and assume the unpublished test suffix sorts older.
Correct: use higher prerelease precedence and retain fail-closed metadata checks.

Wrong: failed Actions -> build locally -> SCP the executable into service.
Correct: stop, report the failed gate, and request the user's next step.

Wrong: missing OAuth token -> conclude SYSTEM cannot publish or must log in as SU.
Correct: inspect credential scope; test an authorized, pinned, non-interactive
SSH path to the same repository without changing the requested Windows identity.
~~~
