#!/usr/bin/env bash
# Maintainer-only, idempotent repository setup. The active gh credential needs
# Administration, Issues and Vulnerability alerts write permissions.
set -euo pipefail
cd "$(dirname "$0")/.."
repo="${1:-hujm2023/rka}"
gh api --method PATCH "repos/$repo" --input .github/repository-settings.json --silent
gh api --method PUT "repos/$repo/topics" --input - --silent <<'JSON'
{"names":["ruankao","software-architecture","learning-platform","ai-tutor","golang","react","docker-compose"]}
JSON
gh label create dependencies --repo "$repo" --color 0366d6 --description 'Dependency updates' --force
gh api --method PUT "repos/$repo/vulnerability-alerts" --silent
gh api --method PUT "repos/$repo/private-vulnerability-reporting" --silent
gh api --method PUT "repos/$repo/branches/main/protection" --input .github/branch-protection.json --silent
printf 'Repository settings applied: %s\n' "$repo"
