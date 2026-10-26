# Updating This Fork

## Commands

- `cc-connect update`: check and install the newest applicable fork release.
- `cc-connect update --check`: fetch release metadata only; no package downloads or executable writes.
- `cc-connect update --pre` (alias `--beta`): retained for compatibility.
- Chat `/upgrade` (alias `/update`): check for an update.
- Chat `/upgrade confirm`: install and request the existing automatic restart.

Chat commands still require `admin_from`. The terminal command does not restart
an existing daemon: restart it after installation. Install this updater-enabled
binary once before relying on these commands; older binaries still use upstream.
Only standalone fork binaries are supported for fork self-update. It deliberately
refuses to overwrite the official npm package's executable.

## Release Contract

`core.UpdateRepository` defaults to `Eitan-S-23/cc-connect`. The fork release
workflow embeds `${GITHUB_REPOSITORY}` into it with `-ldflags -X`. Both commands,
the upgrade card and the nonblocking startup hint use the same updater. There
is no Gitee mirror or upstream fallback on API/download failure.

Fork releases include prereleases by default because this repository publishes
its builds as prereleases. Builds explicitly targeting the official repository
retain stable-only CLI checks unless `--pre` is supplied.

- Tag: `fork-v1.5.1-beta.1+autoretry.1`.
- Embedded version: `v1.5.1-beta.1+autoretry.1`.
- Windows archive: `cc-connect-v1.5.1-beta.1+autoretry.1-windows-amd64.zip`.
- Unix archive: `cc-connect-<version>-<os>-<arch>.tar.gz`.
- Every archive contains exactly one regular file with its expected binary name.
- `checksums.txt` contains the SHA-256 of every archive.

Only published, non-draft `fork-v*` releases with valid version syntax qualify.
Base versions and prerelease identifiers use semver precedence. For equal semver
versions with different build metadata, `published_at` determines fork ordering.
`+notify.1` and `+autoretry.1` must NOT be compared lexicographically. The exact
current tag is fetched separately if it is outside the first 100 release entries.
Missing current-release metadata fails closed instead of guessing a downgrade.
The updater never lowers semver precedence or reinstalls the same normalized tag.

## Installation Contract

`CheckForUpdate(currentVersion string, includePrereleases bool)` returns a
`*ReleaseInfo`, nil when current, or an error. `SelfUpdate(tag string)` installs
that exact tag. Chat confirmation rechecks availability when invoked, then pins
the returned tag throughout download; a new publication cannot change it mid-run.

Before replacement, the updater requires matching published asset names, sizes,
SHA-256 and executable architecture (PE, ELF64 or Mach-O64; amd64/arm64). It checks
the archive member name exactly, rejects extra members and never extracts paths
from the archive to disk. Download and decompression sizes are bounded.

The new binary is staged next to the resolved executable, flushed and renamed
into place after the old binary is renamed to `<executable>.old`. Installation
failure restores the original. If restoration itself fails, the error reports
the preserved backup path. The backup remains after success for manual recovery
and for the existing Linux restart path. Configuration and session files are not
modified by installation.

An exclusive `<executable>.update-lock` protects replacement across processes;
an in-process lock rejects concurrent installs. After a successful install, that
running process must restart before another update. A stale lock after a hard
crash requires inspection before manual removal; it is not blindly deleted.
This is installation rollback, not a watchdog for failures after a new process
has started. Production startup/health verification still belongs to the daemon.

## Validation Matrix

| Case | Expected result |
| --- | --- |
| `notify.1` to later-published `autoretry.1` | Update despite lexical order |
| Same tag or older semver | No install |
| Unknown current same-semver release | Error, never guess |
| API failure or rate limit | Error, no upstream fallback |
| Missing/duplicate asset or checksum | Reject before replacement |
| Corrupt archive, extra member, wrong architecture | Reject before replacement |
| Backup/install rename failure | Keep or restore original executable |
| Restore failure | Preserve backup and report its exact path |
| Non-admin chat command | Reject before network or installation |
| Chat install failure | Report error; do not restart |
| Chat success | Existing restart channel and reply identity preserved |

## Tests

Run `go test -race -tags no_web ./core ./cmd/cc-connect` and the core CUJs.
`TestForkUpdate*` uses an HTTP test server, not production GitHub. It covers six
target formats, publication ordering, source isolation and corrupt downloads.
`TestUpdateReplace*` uses a temporary executable and injected rename failures.
`TestCUJ_U1_ForkUpgradeChecksConfirmsAndRestarts` drives `ReceiveMessage` for
permission denial, check-only behavior, failed confirmation and successful restart.
CLI tests verify the pinned tag, flags, check-only mode and failure receipts.
