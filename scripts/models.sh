#!/usr/bin/env bash
# models.sh — the behaviour models of design/behaviour (`make models`, `make
# models-check`): what each body of the SDK does, derived from the code by
# `kit design derive` (the Studio's ADR 0013). The bodies stay the code's (ADR
# 0168); a model is never edited, it is derived again after a body changes.
#
#   models.sh          derive every package design/behaviour models, and write
#   models.sh -check   write nothing; exit 1 when a model is missing, is not
#                      what the current code derives, or is left over
#
# `kit check` holds the same models through its behaviour rule. Both need kit
# at the version design/sdk.yaml pins, so they are local, like `make regen`.
#
# Runs under macOS's /bin/bash 3.2 and the BSD tools (scripts/CLAUDE.md).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cd "$root"
# shellcheck source=scripts/kit-pinned.sh
. scripts/kit-pinned.sh

case "${1:-}" in
  '')
    kit_pinned models
    kit design derive
    ;;
  -check)
    kit_pinned models-check
    kit design derive -check
    ;;
  *)
    printf 'models: unknown argument %s (usage: models.sh [-check])\n' "$1" >&2
    exit 2
    ;;
esac
