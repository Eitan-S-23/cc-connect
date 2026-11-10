#!/usr/bin/env bash
set -euo pipefail

mode="${1:-go}"
[[ "$mode" == go || "$mode" == web ]]
root="$(git rev-parse --show-toplevel)"
[[ "$(pwd -P)" == "$root" && "$(realpath "$GITHUB_WORKSPACE")" == "$root" ]]
tools="$PROJECT_TOOL_CACHE"
go_dir="$tools/go1.26.3"
go_archive="$tools/go1.26.3.linux-amd64.tar.gz"
node_archive="$tools/node-v20.20.2-linux-x64.tar.xz"
outputs=("$tools" "$go_dir" "$go_archive" "$node_archive" "$tools/node-v20.20.2-linux-x64")
for output in "${outputs[@]}"; do
  case "$(realpath -m "$output")" in
    "$root"/*) ;;
    *) echo "Tool output escapes the source worktree: $output" >&2; exit 1 ;;
  esac
done
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]]
mkdir -p -- "$tools" "$go_dir"

install_archive() {
  local url="$1" checksum="$2" archive="$3" destination="$4" top="$5"
  curl --fail --location --retry 2 --max-time 180 --output "$archive" "$url"
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check --status
  python3 -B - "$archive" "$top" <<'PY'
import posixpath
import sys
import tarfile

archive, top = sys.argv[1:]
def inside(path):
    return path == top or path.startswith(top + "/")
with tarfile.open(archive, "r:*") as bundle:
    for member in bundle:
        name = posixpath.normpath(member.name)
        if member.name.startswith("/") or ".." in member.name.split("/") or not inside(name):
            raise SystemExit("unsafe tool archive member: " + member.name)
        if not (member.isfile() or member.isdir() or member.issym() or member.islnk()):
            raise SystemExit("unsupported tool archive entry: " + member.name)
        if member.issym() or member.islnk():
            target = member.linkname
            if member.issym():
                target = posixpath.join(posixpath.dirname(name), target)
            if member.linkname.startswith("/") or not inside(posixpath.normpath(target)):
                raise SystemExit("unsafe tool archive link: " + member.name)
PY
  tar --extract --file "$archive" --directory "$destination" --no-same-owner
}

install_archive \
  https://go.dev/dl/go1.26.3.linux-amd64.tar.gz \
  2b2cfc7148493da5e73981bffbf3353af381d5f93e789c82c79aff64962eb556 \
  "$go_archive" "$go_dir" go
printf '%s\n' "$go_dir/go/bin" >> "$GITHUB_PATH"
"$go_dir/go/bin/go" version

if [[ "$mode" == web ]]; then
  install_archive \
    https://nodejs.org/dist/v20.20.2/node-v20.20.2-linux-x64.tar.xz \
    df770b2a6f130ed8627c9782c988fda9669fa23898329a61a871e32f965e007d \
    "$node_archive" "$tools" node-v20.20.2-linux-x64
  printf '%s\n' "$tools/node-v20.20.2-linux-x64/bin" >> "$GITHUB_PATH"
  "$tools/node-v20.20.2-linux-x64/bin/node" --version
fi
