# syntax=docker/dockerfile:1
# hadolint global ignore=DL3008
# =============================================================================
# Development Image (Dynamic Layer — rebuilt daily)
# =============================================================================
# Extends the stable base image with frequently-updated tools:
# Claude Code, rtk, kubectl, helm, CodeRabbit, Qodo, etc.
#
# Base image (devcontainer-base) contains: system deps, Cloud CLIs, PPA tools,
# Oh My Zsh. It is rebuilt weekly via Dockerfile.base.
#
# Build:
#   docker buildx build --platform linux/amd64,linux/arm64 \
#     -t ghcr.io/kodflow/devcontainer-template:latest .
# =============================================================================

ARG BASE_IMAGE=ghcr.io/kodflow/devcontainer-base:latest
# hadolint ignore=DL3006
FROM ${BASE_IMAGE}

ARG TARGETARCH
ARG GITHUB_TOKEN=

# CACHE_BUST_DYNAMIC: Forces cache invalidation for dynamic installations.
# Set to current date (YYYY-MM-DD) during scheduled builds to pull latest versions.
ARG CACHE_BUST_DYNAMIC=stable

USER root

# =============================================================================
# Dynamic Binary Tools (rtk) — kubectl/helm/bazelisk/yq are in base
# =============================================================================
RUN RTK_RELEASE=$(curl -fsSL --retry 5 --retry-delay 5 ${GITHUB_TOKEN:+-H "Authorization: token ${GITHUB_TOKEN}"} "https://api.github.com/repos/rtk-ai/rtk/releases/latest") && \
    RTK_LATEST=$(printf '%s' "$RTK_RELEASE" | jq -r '.tag_name') && \
    echo "Installing rtk ${RTK_LATEST}..." && \
    RTK_ARCH=$([ "$TARGETARCH" = "arm64" ] && echo "aarch64-unknown-linux-gnu" || echo "x86_64-unknown-linux-musl") && \
    RTK_URL=$(printf '%s' "$RTK_RELEASE" | jq -r --arg n "rtk-${RTK_ARCH}.tar.gz" '.assets[] | select(.name == $n) | .url') && \
    curl -fsSL --retry 5 --retry-delay 5 -H "Accept: application/octet-stream" ${GITHUB_TOKEN:+-H "Authorization: token ${GITHUB_TOKEN}"} "$RTK_URL" -o /tmp/rtk.tar.gz && \
    tar xzf /tmp/rtk.tar.gz -C /usr/local/bin rtk && \
    rm -f /tmp/rtk.tar.gz

# =============================================================================
# Claude Code (standalone installation - no Node.js required)
# =============================================================================
USER vscode
WORKDIR /home/vscode

RUN curl -fsSL https://claude.ai/install.sh | bash && \
    mkdir -p /home/vscode/.claude/sessions

# =============================================================================
# Claude Configuration (commands, agents, scripts, docs, settings)
# =============================================================================
COPY --chown=vscode:vscode .claude/ /home/vscode/.claude/

# =============================================================================
# System Templates (root-owned, restored at runtime via postStart.sh)
# =============================================================================
USER root

# Claude defaults backup (full directory for volume restoration)
COPY --chown=root:root .claude/ /etc/claude-defaults/

# VPN scripts (staged in /tmp for renaming in consolidated RUN below)
COPY --chown=root:root scripts/vpn/ /tmp/vpn/

# MCP template (secrets injected at runtime via postStart.sh)
# Feature fragments are added to /etc/mcp/features/ by language install.sh scripts
# Image-level fragments live in /etc/mcp/fragments/ (always available)
COPY --chown=vscode:vscode mcp.json.tpl /etc/mcp/mcp.json.tpl
COPY --chown=vscode:vscode mcp-fragments/ /etc/mcp/fragments/

# Template features source — force-synced into /workspace/.devcontainer/features/
# at every postStart (see hooks/lifecycle/postStart.sh step_sync_features).
# The features/ directory and image-template-version.json are staged into the
# build context by CI before build; for local builds run:
#   cp -r .devcontainer/features .devcontainer/images/features
#   printf '{"commit":"local","updated":"1970-01-01T00:00:00Z"}\n' > .devcontainer/images/image-template-version.json
# The version marker lets postStart skip sync when repo is ahead of image.
COPY --chown=root:root features/ /etc/devcontainer-template/features/
COPY --chown=root:root image-template-version.json /etc/devcontainer-template/.template-version

# RTK config template (token savings proxy, root-owned system template)
COPY --chown=root:root rtk.config.toml /etc/rtk/config.toml

# Lifecycle hooks embedded in image (called directly by devcontainer.json)
COPY --chown=root:root hooks/ /etc/devcontainer-hooks/

# Consolidated system setup: permissions, VPN scripts, sudoers
RUN chmod -R 755 /etc/claude-defaults/ /home/vscode/.claude/scripts/ /etc/devcontainer-hooks/ && \
    mkdir -p /etc/mcp/features /etc/mcp/fragments && chown vscode:vscode /etc/mcp/features /etc/mcp/fragments && \
    mkdir -p /etc/openvpn && \
    cp /tmp/vpn/update-dns.sh /etc/openvpn/update-dns && \
    cp /tmp/vpn/vpn-connect.sh /usr/local/bin/vpn-connect && \
    cp /tmp/vpn/vpn-disconnect.sh /usr/local/bin/vpn-disconnect && \
    cp /tmp/vpn/vpn-status.sh /usr/local/bin/vpn-status && \
    rm -rf /tmp/vpn && \
    chmod +x /etc/openvpn/update-dns /usr/local/bin/vpn-connect \
             /usr/local/bin/vpn-disconnect /usr/local/bin/vpn-status && \
    printf '%s\n' \
      'vscode ALL=(ALL) NOPASSWD: /usr/sbin/openvpn' \
      'vscode ALL=(ALL) NOPASSWD: /usr/bin/killall openvpn' \
      'vscode ALL=(ALL) NOPASSWD: /usr/bin/wg' \
      'vscode ALL=(ALL) NOPASSWD: /usr/bin/wg-quick' \
      'vscode ALL=(ALL) NOPASSWD: /usr/sbin/ipsec' \
      'vscode ALL=(ALL) NOPASSWD: /usr/bin/killall charon' \
      'vscode ALL=(ALL) NOPASSWD: /usr/sbin/pppd' \
      'vscode ALL=(ALL) NOPASSWD: /usr/sbin/pptp' \
      > /etc/sudoers.d/vpn && \
    chmod 440 /etc/sudoers.d/vpn

USER vscode

# =============================================================================
# CodeRabbit CLI (AI-powered code review)
# =============================================================================
# Two installer quirks are handled here:
#
# 1. mkdir -p "$HOME/.local/bin" — without it the installer aborts before
#    even attempting to write the binary (parent directory ENOENT).
#
# 2. The installer prints "[SUCCESS] Installation verified" + "[SUCCESS]
#    Installation complete" and then exits non-zero anyway. Trusting the
#    exit code rejects a working install, so we verify the binary
#    artefact ($HOME/.local/bin/coderabbit is executable) and consider
#    that the source of truth. `sh ... || true` keeps the pipeline
#    going; the post-condition is what decides success.
#
# Retry loop is still useful for transient 5xx during download — those
# fail before [SUCCESS] is printed, so the post-condition catches them.
# hadolint ignore=DL4006
RUN mkdir -p "$HOME/.local/bin" && \
    for attempt in 1 2 3 4 5; do \
      rm -f /tmp/cr-install.sh; \
      if curl -fsSL --retry 3 --retry-delay 2 https://cli.coderabbit.ai/install.sh -o /tmp/cr-install.sh; then \
        sh /tmp/cr-install.sh || true; \
      fi; \
      if [ -x "$HOME/.local/bin/coderabbit" ]; then \
        echo "CodeRabbit CLI installed (attempt $attempt)" >&2; \
        rm -f /tmp/cr-install.sh; \
        exit 0; \
      fi; \
      if [ "$attempt" = "5" ]; then \
        echo "CodeRabbit CLI binary missing after 5 attempts" >&2; \
        rm -f /tmp/cr-install.sh; \
        exit 1; \
      fi; \
      echo "CodeRabbit CLI binary missing after attempt $attempt, retrying in $((attempt * 5))s..." >&2; \
      sleep $((attempt * 5)); \
    done

# =============================================================================
# Qodo Command CLI (AI-powered code review + agents)
# =============================================================================
# hadolint ignore=DL3003,DL4006,SC2086
RUN NODE_ARCH=$([ "$TARGETARCH" = "arm64" ] && echo "arm64" || echo "x64") && \
    NODE_VERSION=$(curl -fsSL https://nodejs.org/dist/index.json | jq -r '[.[] | select(.lts != false)][0].version') && \
    curl -fsSL "https://nodejs.org/dist/${NODE_VERSION}/node-${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz" | tar xJ -C /tmp && \
    export PATH="/tmp/node-${NODE_VERSION}-linux-${NODE_ARCH}/bin:$PATH" && \
    npm install -g --prefix="$HOME/.local" @qodo/command@latest && \
    rm -rf "/tmp/node-${NODE_VERSION}-linux-${NODE_ARCH}"

# =============================================================================
# User Tools (status-line, xdg-open)
# =============================================================================
RUN ARCH=$([ "$TARGETARCH" = "arm64" ] && echo "arm64" || echo "amd64") && \
    mkdir -p ~/.local/bin && \
    SL_RELEASE=$(curl -fsSL --retry 5 --retry-delay 5 ${GITHUB_TOKEN:+-H "Authorization: token ${GITHUB_TOKEN}"} "https://api.github.com/repos/kodflow/status-line/releases/latest") && \
    SL_TAG=$(printf '%s' "$SL_RELEASE" | jq -r '.tag_name') && \
    [ -n "$SL_TAG" ] && [ "$SL_TAG" != "null" ] && \
    echo "Installing status-line (${SL_TAG})..." && \
    SL_BIN_URL=$(printf '%s' "$SL_RELEASE" | jq -r --arg n "status-line-linux-${ARCH}" '.assets[] | select(.name == $n) | .url') && \
    SL_SHA_URL=$(printf '%s' "$SL_RELEASE" | jq -r --arg n "status-line-linux-${ARCH}.sha256" '.assets[] | select(.name == $n) | .url') && \
    curl -fsSL --retry 5 --retry-delay 5 -H "Accept: application/octet-stream" ${GITHUB_TOKEN:+-H "Authorization: token ${GITHUB_TOKEN}"} "$SL_BIN_URL" \
      -o ~/.local/bin/status-line && \
    curl -fsSL --retry 5 --retry-delay 5 -H "Accept: application/octet-stream" ${GITHUB_TOKEN:+-H "Authorization: token ${GITHUB_TOKEN}"} "$SL_SHA_URL" \
      -o /tmp/status-line.sha256 && \
    sed -i "s|status-line-linux-${ARCH}|$HOME/.local/bin/status-line|" /tmp/status-line.sha256 && \
    sha256sum -c /tmp/status-line.sha256 && rm -f /tmp/status-line.sha256 && \
    chmod +x ~/.local/bin/status-line && \
    printf '%s\n' '#!/bin/sh' \
    '[ $# -eq 0 ] && { echo "Usage: xdg-open <url> [url...]" >&2; exit 2; }' \
    'for url in "$@"; do printf "\n  Open in browser: %s\n" "$url"; done' \
    'echo ""' \
    > ~/.local/bin/xdg-open && chmod +x ~/.local/bin/xdg-open

# =============================================================================
# Environment Variables
# =============================================================================
ENV TF_PLUGIN_CACHE_DIR=/home/vscode/.cache/terraform \
    HELM_CACHE_HOME=/home/vscode/.cache/helm \
    KUBECONFIG=/home/vscode/.kube/config \
    BAZEL_USER_ROOT=/home/vscode/.cache/bazel \
    CLAUDE_CONFIG_DIR=/home/vscode/.claude \
    ENABLE_TOOL_SEARCH=auto \
    PATH=/home/vscode/.local/bin:$PATH

WORKDIR /workspace

LABEL org.opencontainers.image.source="https://github.com/kodflow/devcontainer-template" \
      org.opencontainers.image.description="Development Image - Claude Code, AI tools, binary tools" \
      org.opencontainers.image.licenses="MIT"
