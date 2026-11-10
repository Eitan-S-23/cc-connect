#!/usr/bin/env bash
set -euo pipefail

root="$(git rev-parse --show-toplevel)"
if [[ "$(pwd -P)" != "$root" || "$(realpath "$GITHUB_WORKSPACE")" != "$root" ]]; then
  echo "Run release steps from the source worktree root." >&2
  exit 1
fi

directories=(
  "$HOME" "$TMPDIR" "$GOCACHE" "$GOMODCACHE" "$GOPATH" "$GOBIN" "$GOTMPDIR"
  "$XDG_CACHE_HOME" "$XDG_CONFIG_HOME" "$XDG_DATA_HOME" "$PNPM_HOME"
  "$NPM_CONFIG_CACHE" "$GH_CONFIG_DIR" "$GH_CACHE_DIR" "$PROJECT_TOOL_CACHE"
  "$root/.cache/pnpm-store" "$root/.cache/pnpm-setup" "$root/dist"
  "$root/web/dist" "$root/web/node_modules"
)
outputs=("${directories[@]}" "$root/web/tsconfig.tsbuildinfo")
for output in "${outputs[@]}"; do
  resolved="$(realpath -m "$output")"
  case "$resolved" in
    "$root"/*) ;;
    *) echo "Release output escapes the source root: $resolved" >&2; exit 1 ;;
  esac
done
mkdir -p -- "${directories[@]}"
echo "Verified project-contained release outputs."
