# Environment & Profile Detection

## Phase 1.0: Environment Detection (MANDATORY)

**MANDATORY: Detect execution context before any operation.**

```yaml
environment_detection:
  1_container_check:
    action: "Detect if running inside container"
    method: "[ -f /.dockerenv ]"
    output: "IS_CONTAINER (true|false)"

  2_devcontainer_check:
    action: "Check DEVCONTAINER env var"
    method: "[ -n \"${DEVCONTAINER:-}\" ]"
    note: "Set by VS Code when attached to devcontainer"

  3_determine_target:
    container_mode:
      target: "/workspace/.devcontainer/images/.claude"
      behavior: "Update template source (requires rebuild)"
      propagation: "Changes applied at next container start"

    host_mode:
      target: "$HOME/.claude"
      behavior: "Update user Claude configuration"
      propagation: "Immediate (no rebuild needed)"

  4_display_context:
    output: |
      Environment: {CONTAINER|HOST}
      Update target: {path}
      Mode: {template|user}
```

**Implementation:**

```bash
# Detect environment context
detect_context() {
    # Check if running inside container
    if [ -f /.dockerenv ]; then
        CONTEXT="container"
        UPDATE_TARGET="/workspace/.devcontainer/images/.claude"
        echo "Detected: Container environment"
    else
        CONTEXT="host"
        UPDATE_TARGET="$HOME/.claude"
        echo "Detected: Host machine"
    fi

    # Additional checks
    if [ -n "${DEVCONTAINER:-}" ]; then
        echo "  (DevContainer detected via DEVCONTAINER env var)"
    fi

    echo "Update target: $UPDATE_TARGET"
    echo "Mode: $CONTEXT"
}

# Call at start of update
detect_context
```

**Output Phase 1.0:**

```
═══════════════════════════════════════════════
  /update - Environment Detection
═══════════════════════════════════════════════

  Environment: HOST MACHINE
  Update target: /home/user/.claude
  Mode: user configuration

  Changes will be:
    - Applied immediately
    - No container rebuild needed
    - Synced to container via postStart.sh

═══════════════════════════════════════════════
```

Or in container:

```
═══════════════════════════════════════════════
  /update - Environment Detection
═══════════════════════════════════════════════

  Environment: DEVCONTAINER
  Update target: /workspace/.devcontainer/images/.claude
  Mode: template source

  Changes will be:
    - Applied to template files
    - Require container rebuild to propagate
    - Or wait for next postStart.sh sync

═══════════════════════════════════════════════
```

---

## Phase 1.5: Profile Detection

**MANDATORY: Auto-detect project profile to determine sync sources. No flags.**

```yaml
profile_detection:
  1_check_directories:
    action: "Check for infrastructure markers"
    checks:
      - "[ -d modules/ ]"
      - "[ -d stacks/ ]"
      - "[ -d ansible/ ]"
    result: "Any directory exists → INFRASTRUCTURE profile"

  2_determine_profile:
    infrastructure:
      condition: "modules/ OR stacks/ OR ansible/ exists"
      sources:
        - "kodflow/devcontainer-template (always)"
        - "kodflow/infrastructure-template (additional)"
      version_files:
        - ".devcontainer/.template-version"
        - ".infra-template-version"

    devcontainer:
      condition: "No infrastructure markers found"
      sources:
        - "kodflow/devcontainer-template (only)"
      version_files:
        - ".devcontainer/.template-version"
```

**Implementation:**

```bash
detect_profile() {
    PROFILE="devcontainer"
    INFRA_DIRS_FOUND=""

    for dir in modules stacks ansible; do
        if [ -d "$dir/" ]; then
            INFRA_DIRS_FOUND="${INFRA_DIRS_FOUND} $dir"
            PROFILE="infrastructure"
        fi
    done

    echo "Profile: $PROFILE"
    if [ "$PROFILE" = "infrastructure" ]; then
        echo "  Infrastructure dirs found:$INFRA_DIRS_FOUND"
        echo "  Sources: devcontainer-template + infrastructure-template"
    else
        echo "  Source: devcontainer-template only"
    fi
}
```

**Output Phase 1.5 (infrastructure detected):**

```
═══════════════════════════════════════════════
  /update - Profile Detection
═══════════════════════════════════════════════

  Profile: infrastructure
  Detected dirs: modules stacks ansible

  Sources:
    - kodflow/devcontainer-template (always)
    - kodflow/infrastructure-template

═══════════════════════════════════════════════
```
