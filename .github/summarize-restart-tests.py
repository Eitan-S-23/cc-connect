"""Record exact Go test outcomes from an Actions-owned project-local log."""
import collections
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
if not path.exists():
    print("::notice::Full test log is absent; full suite was not completed.")
    sys.exit(0)
counts = collections.Counter()
skipped, failed = [], []
for line in path.read_text(encoding="utf-8-sig", errors="replace").splitlines():
    try:
        event = json.loads(line)
    except (ValueError, TypeError):
        continue
    action = event.get("Action")
    if action not in ("pass", "fail", "skip"):
        continue
    name = event.get("Package", "") + "/" + event.get("Test", "[package]")
    counts[("tests_" if event.get("Test") else "packages_") + action] += 1
    if action == "skip":
        skipped.append(name)
    if action == "fail":
        failed.append(name)
summary = {"counts": dict(counts), "skipped": skipped, "failed": failed}
path.with_suffix(".summary.json").write_text(json.dumps(summary, indent=2), encoding="utf-8")
print("::notice title=Go test results::" + json.dumps(summary).replace("%", "%25"))
