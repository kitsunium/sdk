#!/usr/bin/env bash
# install-hooks.sh — wire the in-repo .githooks/ directory as this clone's
# pre-commit hook source. Run once after `git clone`.
#
# Why not symlink into .git/hooks/?
#   .git/ is local-only — no shared template. `core.hooksPath` is the
#   git-native way to point at a repo-tracked directory, so the hook ships
#   with the repo and survives `git clean -fdx`.

set -euo pipefail

WORKSPACE="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
cd "$WORKSPACE"

git config --local core.hooksPath .githooks
echo "✓ core.hooksPath = .githooks (this clone now runs .githooks/pre-commit)"

if [ ! -x .githooks/pre-commit ]; then
    chmod +x .githooks/pre-commit
    echo "✓ chmod +x .githooks/pre-commit"
fi

for f in scripts/pre-commit/*.sh; do
    [ -f "$f" ] || continue
    if [ ! -x "$f" ]; then
        chmod +x "$f"
        echo "✓ chmod +x $f"
    fi
done

echo "Hooks installed. Next commit will run the template + project-local checks."
