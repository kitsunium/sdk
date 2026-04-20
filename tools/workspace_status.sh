#!/usr/bin/env bash
# Emits STABLE_VERSION for Bazel's --stamp mechanism. rules_go rewrites the
# literal `{STABLE_VERSION}` placeholder in each go_library's x_defs at
# link time, mirroring the previous `go build -ldflags -X …Version=<sha>`
# pipeline.
set -eu
if sha=$(git rev-parse HEAD 2>/dev/null); then
    echo "STABLE_VERSION ${sha}"
else
    echo "STABLE_VERSION dev"
fi
