#!/usr/bin/env bash
# The fixture's platforms table: two cells, so a symbol one of them declares
# alone is judged apart.
set -euo pipefail

cat <<'CELLS'
linux/amd64
windows/amd64
CELLS
