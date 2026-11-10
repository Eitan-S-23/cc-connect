# Backend Code-Specs

Read the repository's `AGENTS.md` before implementation. These specifications
describe contracts, not permission to build, install, restart or write elsewhere.

## Pre-Development Checklist

- [Release-first updates](release-first.md): tagged Actions artifacts, full-suite
  gates, unpublished-version ordering and updater-only deployment.

- [Directory-list callbacks](dir-list-callbacks.md): actor identity, bound paths,
  failure atomicity and the existing directory-switch mutation boundary.
- [Container regression](container-regression.md): Windows-to-Linux artifacts,
  embedded shell bytes, isolated output paths and verification levels.
- Read the [test contracts](../unit-test/dir-list.md) when changing either flow.

## Quality Check

Run focused directory and CUJ tests, the full affected packages, `go vet` and
compilation. Distinguish a package pass from a full repository suite pass.

Only spec files exist here. No Trellis task scripts or workflow have been
installed, and task creation or archival must not be invented from these files.
