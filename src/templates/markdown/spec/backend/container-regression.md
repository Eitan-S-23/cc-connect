# Isolated Windows-to-Linux Regression

## 1. Scope / Trigger

Applies when Windows-built Go artifacts are tested on Linux, especially when
`go:embed` includes shell scripts. Isolation is a test strategy, not deployment
authorization and not proof of real messaging-platform interaction.

## 2. Signatures

```text
go test -c -tags goolm -o <approved-task>/core-linux-amd64.test ./core
go test -c -tags goolm -o <approved-task>/feishu-linux-amd64.test ./platform/feishu
core-linux-amd64.test -test.v -test.count=1 -test.timeout=180s
feishu-linux-amd64.test -test.v -test.count=1 -test.timeout=180s
setpriv --bounding-set=-dac_override,-dac_read_search <core-test> -test.run=<permission-test>
GET /api/v1/status (Authorization: Bearer <in-memory management token>)
```

## 3. Contracts

- Determine the local source Git root and the authorized remote output boundary
  before writing. Normalize targets and resolve/reject reparse points or links.
  Explicitly set each command's cwd. Historic examples do not grant permission.
- Keep archives, executables, homes, temporary files, telemetry, logs, caches
  and evidence in approved task directories. Inspect extracted member names and
  verify archive and executable hashes before execution.
- Override `HOME`, Windows `USERPROFILE`/`APPDATA`/`LOCALAPPDATA`, `TEMP`/`TMP`/
  `TMPDIR`, XDG directories, `GOCACHE`, `GOMODCACHE`, `GOPATH`, `GOBIN` and `GOTMPDIR`.
  Set `GOENV=off`, `GOWORK=off`, `GOTOOLCHAIN=local`, `CGO_ENABLED=0` and
  `PYTHONDONTWRITEBYTECODE=1`; clear external Git output overrides and API keys.
- In PowerShell, remove environment entries with `Remove-Item Env:\NAME`.
  An empty `GIT_DIR` is not an unset variable and can break the next invocation's
  Git-root check. Assert that output overrides are absent, not merely blank.
- Embedded `core/runas_probe.sh` must contain LF line endings in the actual
  working copy before compilation. `/core/runas_probe.sh text eol=lf` in
  `.gitattributes` prevents checkout conversion. Git's indexed LF blob alone is
  insufficient: `go:embed` reads the bytes from disk.
- Verify the intended hostname and architecture. Never use a neighboring
  instance because access to the selected one failed.
- Read-only Python audits use `python3 -B`. Standard-library `tomllib` requires
  Python 3.11; use an existing deployment TOML parser on older interpreters,
  rather than installing dependencies or upgrading tools under test-only consent.
- Management status may include nested bridge credentials. Report only
  `data.version`, `data.projects_count` and the count of `data.degraded_platforms`,
  never the full response or configuration.
- Run permission negatives with DAC override/read-search capabilities removed,
  not merely `chmod 000` under unrestricted root. Require an actual pass.
- Do not connect an isolated instance to live Apps, change existing config,
  replace the installed executable or restart production under test-only consent.

## 4. Validation & Error Matrix

| Check | Required action |
| --- | --- |
| Output escapes approved root or unresolved link | Stop before writing |
| Host identity/architecture mismatch | Stop without extraction or execution |
| Archive member or hash mismatch | Stop without executing the artifact |
| Embedded shebang starts `#!/bin/sh\r\n` | Normalize working-copy bytes, rebuild and rerun |
| Root bypasses unreadable fixture | Remove DAC capabilities and require non-skipped pass |
| Empty inherited `GIT_DIR` | Remove the environment entry before resolving the source root |
| Audit Python lacks `tomllib` | Use an already installed parser with bytecode writes disabled |
| Management status includes credentials | Emit only the approved health fields, never raw JSON |
| Package or CUJ failure | Record exact failure, fix scoped cause and rerun |
| Isolated test passes | Report isolated regression only |
| No live button callback observed | Mark real Feishu interaction unverified |

## 5. Good / Base / Bad Cases

- Base: precompiled test binaries execute in a component-local task directory.
- Good: source hashes, test logs and unchanged installed-program hashes provide
  independently checkable evidence without touching live credentials or state.
- Bad: an ELF header or successful `--version` call is labeled an end-to-end
  button test; a skipped permission test is labeled successful coverage.

## 6. Tests Required

- Existing `TestEmbeddedProbeScriptBeginsWithShebang` must pass in the Linux
  artifact, not only in the source checkout or a normalized index view.
- Directory/CUJ tests and the full affected packages pass with project-bound
  temporary output; root-only permission coverage passes separately.
- Final audit compares installed executable, configuration and startup-script
  baselines, checks live health and lists actual task outputs, not just Git status.

## 7. Wrong vs Correct

```text
Wrong: i/lf w/crlf is accepted because Git says the source is LF.
Correct: require w/lf before building; rerun the embedded-byte test on Linux.

Wrong: unit callbacks were simulated, therefore Feishu button clicks are verified.
Correct: report automatic callback/CUJ regression and real client interaction separately.
```
