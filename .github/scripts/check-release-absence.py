"""Fail closed on API errors; a published version may not be overwritten."""
import json
import os
import subprocess

result = subprocess.run([
    "gh", "api", f"repos/{os.environ['GITHUB_REPOSITORY']}/releases/tags/{os.environ['RELEASE_TAG']}",
], capture_output=True, text=True)
if result.returncode == 0:
    raise SystemExit("Release already exists; publish a new version instead")
try:
    missing = str(json.loads(result.stdout).get("status")) == "404"
except (ValueError, AttributeError):
    missing = False
if not missing:
    raise SystemExit("Cannot verify release absence; check API access before publishing")
print("Release tag has not been published")
