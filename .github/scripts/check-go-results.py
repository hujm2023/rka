"""A successful CI run must exercise integration tests, not silently skip them."""
import json
import sys
from pathlib import Path

rows = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines()]
failed = [row for row in rows if row.get("Action") == "fail" or (row.get("Action") == "skip" and "Test" in row)]
passed = [row for row in rows if row.get("Action") == "pass" and "Test" in row]
if failed or not passed:
    for row in failed:
        print(row["Action"], row.get("Package", ""), row.get("Test", ""))
    raise SystemExit("CI requires passing tests with zero failures and zero skipped tests")
print(f"Passed {len(passed)} tests including subtests; zero failures and skipped tests")
