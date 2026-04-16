#!/usr/bin/env bash
# Shared setup for all bats tests

# Path to scripts under test
SCRIPTS_DIR="${BATS_TEST_DIRNAME}/../../.devcontainer/images/.claude/scripts"

# Call from each test file's setup()
common_setup() {
    TEST_TMPDIR="$(mktemp -d)"
    export TEST_TMPDIR
}

# Call from each test file's teardown()
common_teardown() {
    [ -d "${TEST_TMPDIR:-}" ] && rm -rf "$TEST_TMPDIR"
}

# Source common.sh for tests that need it
load_common() {
    # shellcheck source=/dev/null
    source "$SCRIPTS_DIR/common.sh"
}
