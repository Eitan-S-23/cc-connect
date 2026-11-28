#!/usr/bin/env bash
set -euo pipefail
# Identical synthetic corpus, toolchain, runner and flags for both versions.
# Isolated fixtures never read or rewrite a real user's Codex sessions.
upstream_commit=ba299781cb24c8839669cc0d971aff563e06c533
reference=.cache/official-list-benchmark
mkdir -p .cache/verification
git fetch --no-tags --depth=1 https://github.com/chenhg5/cc-connect.git "$upstream_commit"
git worktree add --detach "$reference" "$upstream_commit"
cp agent/codex/list_bench_test.go "$reference/agent/codex/custom_list_bench_test.go"
go test -tags 'goolm no_web' ./agent/codex -run '^$' -bench '^BenchmarkListSessions$' -benchtime=3x -count=3 | tee .cache/verification/list-custom.txt
go -C "$reference" test -tags 'goolm no_web' ./agent/codex -run '^$' -bench '^BenchmarkListSessions$' -benchtime=3x -count=3 | tee .cache/verification/list-official.txt
python3 - <<'PY'
import os, pathlib, re, statistics
results = {}
for name in ('custom', 'official'):
    text = pathlib.Path(f'.cache/verification/list-{name}.txt').read_text()
    values = re.findall(r'^BenchmarkListSessions-\d+\s+\d+\s+(\d+) ns/op', text, re.M)
    if len(values) != 3:
        raise SystemExit(f'Missing benchmark samples: {name}')
    results[name] = statistics.median(map(int, values))/1e6
summary = (f'Codex ListSessions, 800 files / 200 matching / 40 records per file. '
           f'Median of 3 samples of 3 iterations (first iteration includes source patching): '
           f'custom {results["custom"]:.2f} ms, official v1.5.4 {results["official"]:.2f} ms; '
           f'{results["official"]/results["custom"]:.2f}x speed ratio. '
           'Synthetic results do not predict every real session store.')
print(summary)
print('::notice title=Codex session listing benchmark::'+summary)
with open(os.environ['GITHUB_STEP_SUMMARY'], 'a') as output:
    output.write(summary+'\n')
PY
