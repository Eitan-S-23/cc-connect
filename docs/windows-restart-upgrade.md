# Windows restart and upgrade lifecycle

This change is source work and isolated regression coverage. It does not install
a program, publish a Release, modify an existing scheduled task, or restart a
production instance.

## Problem and evidence

The old Windows `/restart` started a child replacement with `exec.Command.Start`.
The long-lived `_daemon-supervise` process continued mapping the previous
executable after the updater renamed it to `.old`. A later upgrade deleted that
fixed backup first and failed with `remove previous backup: Access is denied`.
`/upgrade confirm` used the same restart channel. Its old log wording did not
distinguish an automatic upgrade restart from a manual command.

The old supervisor also cleans its child's Job Object when that child exits.
A replacement launched inside the old tree could be cleaned with it. This is a
separate integration risk; the user's historical PID snapshot alone does not
prove it happened. ExecutablePath and CommandLine do not reliably identify a
renamed mapped image. Inspect the main module mapping when diagnosing `.old`.

## Restart ownership

The Windows supervised path validates its supervisor identity (PID and creation
time) and binds the running ancestor's executable and parsed arguments to one
running Task Scheduler action. It requires exactly one supervisor in that
task's tree and one task action. The task definition is fingerprinted and checked
again before stopping/starting it. Ambiguous, detached or shared launchers fail
before installation with an explicit diagnostic; no guessed task or hardcoded
local path/PID is used.

An independent, temporary scheduled task runs `_daemon-restart-controller` under
the current identity. It is outside the old business/supervisor Job Object. It
uses the last working executable when an update is pending, so a new executable
that cannot start does not also disable the recovery controller. The normal
task's action, Python or PowerShell, is reused unchanged.

The controller acknowledges preparation before the business process shuts down.
After services stop and restart notification/state are saved, the old process
commits shutdown and exits without spawning a replacement. The controller stops
the bound task, waits for the old process handles to signal exit and the task to
leave Running, and starts the original task. Polling has deadlines; elapsed
fixed sleeps are not treated as proof of exit. A failed stop never starts another
instance. PowerShell operations have their own bounded timeout.

The new supervisor captures its version, digest and process identity once at
startup, passes them to the child, and retains its existing single-instance and
Job Object behavior. After the child's engines and state are restored, its
version and startup digest must match the supervisor and requested version.
The controller verifies both new identities are alive and different from the
old identities before committing activation. Platform notifications still wait
for the target platform to be ready. Installation and activation have distinct
i18n messages; a failed validation cannot emit a success notification.

Non-supervised Windows startup keeps the existing direct restart behavior.
Linux/macOS retain their existing `exec` and replacement logic.

## Update transactions and recovery

Only Windows changes its backup layout. For an executable `bot.exe`, a verified
download installs through `bot.exe.updates/<128-bit-random-id>/` containing:

- `receipt.json`: target, transaction ID, expected version, old/new SHA-256,
  timestamp and state;
- `previous.exe`: rollback copy;
- `failed.exe`: failed new image, when startup rollback was needed.

The receipt is persisted through temporary-file replacement. Installation is
serialized with the existing exclusive update lock. A pending transaction blocks
another installation until activation or recovery. Installation failure restores
the previous executable; rollback failure reports the surviving backup path.
Startup failure stops the failed task before restoring the checked old image
and starting the original task. Recovery keeps a failure receipt and suppresses
the old generic restart-success notification. Starting the recovered task is
reported separately from verifying its health.

The old unmanaged `.old` file is never reused or automatically deleted. The two
newest transaction directories are retained. Older owned images are validated
against their receipts before cleanup. Windows Restart Manager identifies live
users of a historical image; occupied histories are retained for later cleanup.
An access-denied deletion alone is not classified as occupancy. Unexpected files,
links/reparse points, checksum changes, real permission errors and cleanup errors
are reported. A maximum of 12 retained histories prevents unlimited accumulation;
when occupancy prevents further cleanup, installation stops with a diagnostic.

Restart metadata is under `.<config basename>.restart` beside the configuration.
It contains the bound task fingerprint, identity/handshake records and result.
An unresolved receipt is retained for diagnosis instead of being overwritten by
a duplicate request. Completed known artifacts are removed before the next
restart, after the old controller has exited. Never delete this directory merely
to bypass a running controller or unresolved transaction.

## Verification and limitations

`.github/workflows/windows-restart.yml` runs on the development branch only. It
publishes test evidence artifacts, not a Release. All selected build, cache,
temporary and evidence outputs are inside the Actions workspace, with Windows
reparse-point checks before creation. A checksum-pinned Go toolchain is installed
there. Windows integration tests require `CC_WINDOWS_TASK_TESTS=1`; absence of
that opt-in is an explicit skip, never evidence of a process-level pass.

The fixed-`.old` regression is copied unchanged to baseline `e2b9795` and must
fail with its specific mapped-backup assertion. Fixed-source regressions cover
real mapped executables, deferred cleanup, actual ACL denial, transaction errors,
activation notifications and real isolated scheduled tasks. Test tasks run only
test executables and synthetic state; they never parse live credentials or
connect to a bot. PowerShell and Python fixture launchers exercise reuse of an
existing task action. Existing Job Object cleanup tests remain required.

The private/local `windows-startup/cc_launcher.py` and the user's Windows checkout
are not present in the Linux development workspace. Generic Python fixture
coverage does not establish compatibility with that exact script. A launcher
that detaches, changes task arguments while running, hosts multiple supervisors,
or uses credentials unavailable to the current identity will be rejected and
needs a separately reviewed integration. Full Windows and Linux suite outcomes,
skips and any baseline failures must be recorded with the exact Actions commit;
source completion is not equivalent to successful verification or deployment.

## First installation from the affected old release

The old installed updater cannot acquire the new recovery behavior before it
has installed the new release. Its existing `.old` lock may block that first
installation.

1. Obtain a maintenance authorization for the specific Windows task/config,
   covering interruption of current work and potentially two complete task
   restarts. Read current identities and actual module mappings again. Historical
   PIDs must never be used as kill targets.
2. Stop the current scheduled task through the established entry point. Confirm
   the old business process and supervisor actually exited and released the
   mapped backup. Start the original task. Do not change ACLs or manually replace
   executable files to evade the updater.
3. Follow the release gate: exact source commit/tag, required CI passing, Actions
   builds, published non-draft Release in the authorized repository, verified
   version/assets/checksums, then the corresponding bot's `/upgrade confirm`.
4. The old updater may leave its old supervisor running after installing the
   fixed main process. A new main under a legacy supervisor must withhold
   activation success and refuse further managed upgrades. If detected, perform
   the second authorized complete task restart through the original launcher.
5. Verify the running supervisor and main both use the intended release, one
   instance owns the configuration, platform connectivity is restored, and
   configuration, sessions and cron state remain intact. Then validate the next
   upgrade using the corrected lifecycle in a later maintenance window.

Publishing a Release does not authorize these steps. No local build, downloaded
executable copy, alternate update channel, or direct overwrite substitutes for
the built-in updater and the separate maintenance authorization.
