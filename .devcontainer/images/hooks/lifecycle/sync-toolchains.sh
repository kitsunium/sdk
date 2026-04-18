#!/bin/bash
# ============================================================================
# sync-toolchains.sh — Sync toolchains from image to volume on version mismatch
# Called by onCreateCommand (volumes already mounted)
#
# Problem: Features install to ~/.cache/ during build. Volume mounts OVER that
# path at runtime. On rebuild, the volume has OLD tools.
#
# Solution: Features write version markers to /opt/devcontainer-versions/.
# This script compares with volume markers and re-installs only on mismatch.
# ============================================================================

set -e

VERSIONS_DIR="/opt/devcontainer-versions"
CACHE_DIR="${HOME}/.cache"

[ -d "$VERSIONS_DIR" ] || exit 0

sync_lang() {
    local lang="$1"
    local image_ver volume_ver

    image_ver=$(cat "${VERSIONS_DIR}/${lang}" 2>/dev/null) || return 0
    volume_ver=$(cat "${CACHE_DIR}/.toolchain-version-${lang}" 2>/dev/null || echo "")

    if [ "$image_ver" = "$volume_ver" ]; then
        echo "  ${lang}: up to date (volume cache hit)"
        return 0
    fi

    echo "  ${lang}: version mismatch (image=${image_ver} volume=${volume_ver:-none})"
    echo "  ${lang}: syncing from image..."

    case "$lang" in
        rust)
            sync_rust
            ;;
        go)
            sync_go
            ;;
        python)
            sync_python
            ;;
        node)
            sync_node
            ;;
        *)
            echo "  ${lang}: no sync handler, skipping"
            return 0
            ;;
    esac

    echo "$image_ver" > "${CACHE_DIR}/.toolchain-version-${lang}"
    echo "  ${lang}: synced"
}

sync_rust() {
    export CARGO_HOME="${CARGO_HOME:-${CACHE_DIR}/cargo}"
    export RUSTUP_HOME="${RUSTUP_HOME:-${CACHE_DIR}/rustup}"
    export PATH="${CARGO_HOME}/bin:${PATH}"

    if ! command -v rustup &>/dev/null; then
        curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --no-modify-path 2>/dev/null
    fi

    [ -f "${CARGO_HOME}/env" ] && source "${CARGO_HOME}/env"
    rustup toolchain install stable --profile minimal 2>/dev/null
    rustup default stable 2>/dev/null
    rustup component add rust-analyzer clippy rustfmt 2>/dev/null

    if command -v cargo-binstall &>/dev/null || {
        curl -L --proto '=https' --tlsv1.2 -sSf \
            https://raw.githubusercontent.com/cargo-bins/cargo-binstall/main/install-from-binstall-release.sh | bash 2>/dev/null
    }; then
        local tools=(cargo-watch cargo-nextest cargo-deny)
        for tool in "${tools[@]}"; do
            command -v "$tool" &>/dev/null || cargo binstall --no-confirm --locked "$tool" 2>/dev/null || true
        done
        local binstall_only=(cargo-outdated cargo-tarpaulin wasm-bindgen-cli cargo-expand)
        for tool in "${binstall_only[@]}"; do
            command -v "$tool" &>/dev/null || cargo binstall --no-confirm --disable-strategies compile "$tool" 2>/dev/null || true
        done
    fi
}

sync_go() {
    export GOROOT="${GOROOT:-/usr/local/go}"
    export GOPATH="${GOPATH:-${CACHE_DIR}/go}"
    export GOCACHE="${GOCACHE:-${CACHE_DIR}/go-build}"
    export PATH="${GOROOT}/bin:${GOPATH}/bin:${PATH}"

    mkdir -p "${GOPATH}/bin"

    if ! command -v go &>/dev/null; then
        echo "  go: binary missing (requires rebuild)"
        return 1
    fi

    local ARCH
    ARCH=$(uname -m)
    local GO_ARCH
    case "$ARCH" in
        x86_64) GO_ARCH="amd64" ;;
        aarch64|arm64) GO_ARCH="arm64" ;;
        *) GO_ARCH="amd64" ;;
    esac

    local tools=(golangci-lint gosec gofumpt gotestsum goimports ktn-linter)
    for tool in "${tools[@]}"; do
        if ! command -v "$tool" &>/dev/null; then
            echo "    installing ${tool}..."
            case "$tool" in
                goimports)
                    go install golang.org/x/tools/cmd/goimports@latest 2>/dev/null || true
                    ;;
                *)
                    go install "${tool}@latest" 2>/dev/null || true
                    ;;
            esac
        fi
    done
}

sync_python() {
    if ! command -v python3 &>/dev/null; then
        echo "  python: binary missing (requires rebuild)"
        return 1
    fi
    local tools=(ruff pylint mypy bandit pytest)
    for tool in "${tools[@]}"; do
        command -v "$tool" &>/dev/null || python3 -m pip install --quiet "$tool" 2>/dev/null || true
    done
}

sync_node() {
    if ! command -v node &>/dev/null; then
        echo "  node: binary missing (requires rebuild)"
        return 1
    fi
}

echo "Syncing toolchains (image → volume)..."
for marker in "${VERSIONS_DIR}"/*; do
    [ -f "$marker" ] || continue
    lang=$(basename "$marker")
    sync_lang "$lang"
done
echo "Toolchain sync complete."
