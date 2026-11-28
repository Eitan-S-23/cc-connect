#!/usr/bin/env bash
# Surface actionable failures through public check annotations, so development
# authenticated over SSH can diagnose Actions without a separate API token.
set -euo pipefail
label="$1"
shift
mkdir -p .cache/verification
log=".cache/verification/${label}.log"
if "$@" >"$log" 2>&1; then
  cat "$log"
else
  result=$?
  cat "$log"
  python3 - "$label" "$log" <<'PY'
import pathlib, sys
label, path = sys.argv[1:]
lines = pathlib.Path(path).read_text(errors='replace').splitlines()
indices = [i for i, line in enumerate(lines) if any(marker in line for marker in ('--- FAIL:', 'WARNING: DATA RACE', 'panic:', 'fatal error:'))]
if not indices:
    indices = [0]
for i in indices[:20]:
    text = '\n'.join(lines[max(0, i-2):i+65])[:14000]
    text = text.replace('%', '%25').replace('\r', '%0D').replace('\n', '%0A')
    print(f'::error title={label} failure::{text}')
PY
  exit "$result"
fi
