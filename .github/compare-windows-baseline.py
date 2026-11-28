"""Compare observed failures without turning a failed full suite into success."""
import json
import pathlib
import sys

fixed_path, baseline_path = map(pathlib.Path, sys.argv[1:])
fixed = json.loads(fixed_path.read_text(encoding="utf-8"))
baseline = json.loads(baseline_path.read_text(encoding="utf-8"))
fixed_failed, baseline_failed = set(fixed["failed"]), set(baseline["failed"])
comparison = {
    "fixed_log": fixed_path.name,
    "baseline_log": baseline_path.name,
    "shared_failures": sorted(fixed_failed & baseline_failed),
    "fixed_only_failures": sorted(fixed_failed - baseline_failed),
    "baseline_only_failures": sorted(baseline_failed - fixed_failed),
    "fixed_unfinished": fixed.get("unfinished", []),
    "baseline_unfinished": baseline.get("unfinished", []),
}
fixed_path.with_suffix(".comparison.json").write_text(
    json.dumps(comparison, indent=2), encoding="utf-8"
)
print("::notice title=Windows baseline comparison::" + json.dumps(comparison).replace("%", "%25"))
