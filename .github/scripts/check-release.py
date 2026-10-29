"""Allow versioned releases only from an existing tag on main's history."""
import json
import os
import re
import subprocess
import sys
from pathlib import Path


def validate(tag, package_version):
    match = re.fullmatch(r"v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(alpha|beta|rc)\.[1-9]\d*)?", tag)
    if not match:
        raise ValueError("Expected vX.Y.Z or vX.Y.Z-{alpha,beta,rc}.N")
    if tag[1:].split("-")[0] != package_version:
        raise ValueError("Tag base version must match frontend/package.json")
    return "-" in tag


if "--self-test" in sys.argv:
    assert not validate("v0.1.0", "0.1.0")
    assert validate("v0.1.0-rc.1", "0.1.0")
    for tag in ["main", "v01.1.0", "v0.1.0;echo bad", "v0.2.0", "v0.1.0-rc.0"]:
        try:
            validate(tag, "0.1.0")
        except ValueError:
            continue
        raise AssertionError(tag)
    print("Release tag validation passed")
    raise SystemExit(0)


def git(*args):
    return subprocess.check_output(["git", *args], text=True).strip()


tag = os.environ["RELEASE_TAG"]
prerelease = validate(tag, json.loads(Path("frontend/package.json").read_text())["version"])
assert git("rev-parse", f"refs/tags/{tag}^{{commit}}") == git("rev-parse", "HEAD"), "Checkout must match tag"
subprocess.run(["git", "merge-base", "--is-ancestor", "HEAD", "origin/main"], check=True)
repository = os.environ["GITHUB_REPOSITORY"]
assert re.fullmatch(r"[\w.-]+/[\w.-]+", repository)
with open(os.environ["GITHUB_OUTPUT"], "a") as output:
    output.write(f"tag={tag}\nprerelease={str(prerelease).lower()}\nimage=ghcr.io/{repository.lower()}\n")
print(f"Validated {tag} on main history")
