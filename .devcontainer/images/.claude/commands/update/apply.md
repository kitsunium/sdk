# Apply Changes

## Phase 4.0: Extract & Apply (From Tarball)

**Copy files from extracted tarballs to their destinations.
No per-file HTTP validation needed: the tarball is already validated.**

```yaml
extract_workflow:
  rule: "Copy from extracted tarball, validate non-empty"

  devcontainer_extract:
    strategy: "cp from extract dir to local paths"
    compose_strategy: "REPLACE devcontainer service, PRESERVE custom"

  infra_extract:
    strategy: "cp with protected path filtering"
    skip_protected: true
```

**Implementation:**

```bash
# Check if a path is protected (for infra sync)
is_protected() {
    local file_path="$1"
    for protected in $PROTECTED_PATHS; do
        case "$file_path" in
            "$protected"*) return 0 ;;
            */"$protected") return 0 ;;
        esac
        local bn
        bn=$(basename "$file_path")
        if [ "$bn" = "$protected" ]; then
            return 0
        fi
    done
    return 1
}

# Copy devcontainer components from extracted tarball
# Safe glob copy: copies matching files or silently skips if no match
# Usage: safe_glob_copy <pattern> <dest_dir> [+x]
safe_glob_copy() {
    local pattern="$1" dest="$2" make_exec="${3:-}"
    local found=0
    # Use find to avoid glob expansion failures under set -e
    local dir=$(dirname "$pattern")
    local glob=$(basename "$pattern")
    while IFS= read -r -d '' f; do
        cp -f "$f" "$dest/"
        [ "$make_exec" = "+x" ] && chmod +x "$dest/$(basename "$f")"
        found=1
    done < <(find "$dir" -maxdepth 1 -name "$glob" -type f -print0 2>/dev/null)
    return 0
}

apply_devcontainer_tarball() {
    local src="$EXTRACT_DIR"

    # Scripts (hooks)
    if [ -d "$src/.devcontainer/images/.claude/scripts" ]; then
        mkdir -p "$UPDATE_TARGET/scripts"
        safe_glob_copy "$src/.devcontainer/images/.claude/scripts/*.sh" "$UPDATE_TARGET/scripts" "+x"
        echo "  ✓ hooks"
    fi

    # Commands (top-level)
    if [ -d "$src/.devcontainer/images/.claude/commands" ]; then
        mkdir -p "$UPDATE_TARGET/commands"
        safe_glob_copy "$src/.devcontainer/images/.claude/commands/*.md" "$UPDATE_TARGET/commands"
        echo "  ✓ commands"
    fi

    # Command sub-modules (subdirectories like commands/git/, commands/search/, etc.)
    if [ -d "$src/.devcontainer/images/.claude/commands" ]; then
        while IFS= read -r -d '' subdir; do
            local rel="${subdir#$src/.devcontainer/images/.claude/}"
            mkdir -p "$UPDATE_TARGET/$rel"
            safe_glob_copy "$subdir/*.md" "$UPDATE_TARGET/$rel"
        done < <(find "$src/.devcontainer/images/.claude/commands" -mindepth 1 -type d -print0 2>/dev/null)
        echo "  ✓ command sub-modules"
    fi

    # Agents
    if [ -d "$src/.devcontainer/images/.claude/agents" ]; then
        mkdir -p "$UPDATE_TARGET/agents"
        safe_glob_copy "$src/.devcontainer/images/.claude/agents/*.md" "$UPDATE_TARGET/agents"
        echo "  ✓ agents"
    fi

    # Lifecycle stubs (container only)
    if [ "$CONTEXT" = "container" ] && [ -d "$src/.devcontainer/hooks/lifecycle" ]; then
        mkdir -p ".devcontainer/hooks/lifecycle"
        safe_glob_copy "$src/.devcontainer/hooks/lifecycle/*.sh" ".devcontainer/hooks/lifecycle" "+x"
        echo "  ✓ lifecycle"
    fi

    # Image-embedded hooks (container only)
    if [ "$CONTEXT" = "container" ] && [ -d "$src/.devcontainer/images/hooks" ]; then
        mkdir -p ".devcontainer/images/hooks/shared" ".devcontainer/images/hooks/lifecycle"
        [ -f "$src/.devcontainer/images/hooks/shared/utils.sh" ] && \
            cp -f "$src/.devcontainer/images/hooks/shared/utils.sh" ".devcontainer/images/hooks/shared/utils.sh" && \
            chmod +x ".devcontainer/images/hooks/shared/utils.sh"
        safe_glob_copy "$src/.devcontainer/images/hooks/lifecycle/*.sh" ".devcontainer/images/hooks/lifecycle" "+x"
        echo "  ✓ image-hooks"
    fi

    # Shared utils (container only - needed by initialize.sh on host)
    if [ "$CONTEXT" = "container" ] && [ -f "$src/.devcontainer/hooks/shared/utils.sh" ]; then
        cp -f "$src/.devcontainer/hooks/shared/utils.sh" ".devcontainer/hooks/shared/utils.sh"
        echo "  ✓ shared-utils"
    fi

    # p10k (container only)
    if [ "$CONTEXT" = "container" ] && [ -f "$src/.devcontainer/images/.p10k.zsh" ]; then
        cp -f "$src/.devcontainer/images/.p10k.zsh" ".devcontainer/images/.p10k.zsh"
        echo "  ✓ p10k"
    fi

    # settings.json
    if [ -f "$src/.devcontainer/images/.claude/settings.json" ]; then
        cp -f "$src/.devcontainer/images/.claude/settings.json" "$UPDATE_TARGET/settings.json"
        echo "  ✓ settings"
    fi

    # grepai config (container only)
    if [ "$CONTEXT" = "container" ] && [ -f "$src/.devcontainer/images/grepai.config.yaml" ]; then
        cp -f "$src/.devcontainer/images/grepai.config.yaml" ".devcontainer/images/grepai.config.yaml"
        echo "  ✓ grepai"
    fi

    # MCP template (container only)
    if [ "$CONTEXT" = "container" ] && [ -f "$src/.devcontainer/images/mcp.json.tpl" ]; then
        mkdir -p ".devcontainer/images"
        cp -f "$src/.devcontainer/images/mcp.json.tpl" ".devcontainer/images/mcp.json.tpl"
        echo "  ✓ mcp-template"
    fi

    # MCP fragments (container only)
    if [ "$CONTEXT" = "container" ] && [ -d "$src/.devcontainer/images/mcp-fragments" ]; then
        mkdir -p ".devcontainer/images/mcp-fragments"
        safe_glob_copy "$src/.devcontainer/images/mcp-fragments/*.json" ".devcontainer/images/mcp-fragments"
        echo "  ✓ mcp-fragments"
    fi

    # Design patterns docs
    if [ -d "$src/.devcontainer/images/.claude/docs" ]; then
        mkdir -p "$UPDATE_TARGET/docs"
        cp -rf "$src/.devcontainer/images/.claude/docs/"* "$UPDATE_TARGET/docs/"
        echo "  ✓ docs"
    fi

    # Templates (project, terraform, docs)
    if [ -d "$src/.devcontainer/images/.claude/templates" ]; then
        mkdir -p "$UPDATE_TARGET/templates"
        cp -rf "$src/.devcontainer/images/.claude/templates/"* "$UPDATE_TARGET/templates/"
        echo "  ✓ templates"
    fi

    # devcontainer.json (container only - update feature refs)
    if [ "$CONTEXT" = "container" ] && [ -f "$src/.devcontainer/devcontainer.json" ]; then
        cp -f "$src/.devcontainer/devcontainer.json" ".devcontainer/devcontainer.json"
        echo "  ✓ devcontainer.json"
    fi

    # Dockerfile (container only - update FROM reference)
    if [ "$CONTEXT" = "container" ] && [ -f "$src/.devcontainer/Dockerfile" ]; then
        cp -f "$src/.devcontainer/Dockerfile" ".devcontainer/Dockerfile"
        echo "  ✓ Dockerfile"
    fi

    # .vscode/settings.json (file nesting + editor defaults — force overwrite)
    if [ -f "$src/.vscode/settings.json" ]; then
        mkdir -p ".vscode"
        cp -f "$src/.vscode/settings.json" ".vscode/settings.json"
        echo "  ✓ vscode (settings.json)"
    fi

    # docker-compose.yml (container only, preserve custom services)
    if [ "$CONTEXT" = "container" ]; then
        update_compose_from_tarball "$src"
    fi
}

# Copy infrastructure components with protected path filtering
apply_infra_tarball() {
    if [ "$PROFILE" != "infrastructure" ]; then
        return 0
    fi

    local src="$INFRA_EXTRACT_DIR"
    local synced=0
    local skipped=0

    echo ""
    echo "  Infrastructure components:"

    for component in $INFRA_COMPONENTS; do
        if [ ! -d "$src/$component" ]; then
            continue
        fi

        local comp_count=0
        while IFS= read -r -d '' src_file; do
            local rel_path="${src_file#$src/}"

            # Always skip protected paths (prevents restoring deleted files)
            if is_protected "$rel_path"; then
                skipped=$((skipped + 1))
                continue
            fi

            mkdir -p "$(dirname "$rel_path")"
            cp -f "$src_file" "$rel_path"
            synced=$((synced + 1))
            comp_count=$((comp_count + 1))

            case "$rel_path" in
                *.sh) chmod +x "$rel_path" ;;
            esac
        done < <(find "$src/$component" -type f -print0 2>/dev/null)

        echo "    ✓ $component/ ($comp_count files)"
    done

    echo "  Synced: $synced files, Protected: $skipped skipped"
}
```

---

## Phase 5.0: Synthesize (Tarball Orchestration)

**Orchestrates the full update using tarball downloads.**

### 5.1: Download tarballs

```bash
# 1. Always: devcontainer template tarball (1 API call)
DEVCONTAINER_TARBALL_URL="https://api.github.com/repos/kodflow/devcontainer-template/tarball/main"
download_tarball "$DEVCONTAINER_TARBALL_URL" "devcontainer-template"
DEVCONTAINER_EXTRACT_DIR="$EXTRACT_DIR"

# 2. If infrastructure profile: infrastructure template tarball (1 API call)
if [ "$PROFILE" = "infrastructure" ]; then
    INFRA_TARBALL_URL="https://api.github.com/repos/kodflow/infrastructure-template/tarball/main"
    download_tarball "$INFRA_TARBALL_URL" "infrastructure-template"
    INFRA_EXTRACT_DIR="$EXTRACT_DIR"
fi
```

### 5.2: Apply devcontainer components

```bash
echo ""
echo "Applying devcontainer components..."
EXTRACT_DIR="$DEVCONTAINER_EXTRACT_DIR"
apply_devcontainer_tarball
```

### 5.3: docker-compose.yml merge (from tarball)

```bash
# Update compose from tarball (REPLACE devcontainer service, PRESERVE custom services)
# Note: Uses mikefarah/yq (Go version)
# Ollama runs on HOST (installed via initialize.sh), not in container
update_compose_from_tarball() {
    local src="$1"
    local compose_file=".devcontainer/docker-compose.yml"
    local template_compose="$src/.devcontainer/docker-compose.yml"

    if [ ! -f "$template_compose" ]; then
        echo "  ⚠ No docker-compose.yml in tarball"
        return 1
    fi

    if [ ! -f "$compose_file" ]; then
        cp "$template_compose" "$compose_file"
        echo "  ✓ docker-compose.yml created from template"
        return 0
    fi

    local temp_services=$(mktemp --suffix=.yaml)
    local temp_volumes=$(mktemp --suffix=.yaml)
    local temp_networks=$(mktemp --suffix=.yaml)
    local backup_file="${compose_file}.backup"

    # Backup original
    cp "$compose_file" "$backup_file"

    # Extract custom services (anything that's NOT devcontainer)
    yq '.services | to_entries | map(select(.key != "devcontainer")) | from_entries' \
        "$compose_file" > "$temp_services"

    # Extract custom volumes and networks
    yq '.volumes // {}' "$compose_file" > "$temp_volumes"
    yq '.networks // {}' "$compose_file" > "$temp_networks"

    # Start fresh from template
    cp "$template_compose" "$compose_file"

    # Merge back custom services if any exist
    if [ -s "$temp_services" ] && [ "$(yq '. | length' "$temp_services")" != "0" ]; then
        yq -i ".services *= load(\"$temp_services\")" "$compose_file"
        echo "    - Preserved custom services"
    fi

    # Merge back custom volumes if any exist
    if [ -s "$temp_volumes" ] && [ "$(yq '. | length' "$temp_volumes")" != "0" ]; then
        yq -i ".volumes *= load(\"$temp_volumes\")" "$compose_file"
        echo "    - Preserved custom volumes"
    fi

    # Merge back custom networks if any exist
    if [ -s "$temp_networks" ] && [ "$(yq '. | length' "$temp_networks")" != "0" ]; then
        yq -i ".networks *= load(\"$temp_networks\")" "$compose_file"
        echo "    - Preserved custom networks"
    fi

    rm -f "$temp_services" "$temp_volumes" "$temp_networks"

    # Verify
    if [ -s "$compose_file" ] && yq '.services.devcontainer' "$compose_file" > /dev/null 2>&1; then
        rm -f "$backup_file"
        echo "  ✓ docker-compose.yml updated (devcontainer replaced, custom preserved)"
        return 0
    else
        mv "$backup_file" "$compose_file"
        echo "  ✗ Compose validation failed, restored backup"
        return 1
    fi
}
```

### 5.4: Apply infrastructure components

```bash
# If infrastructure profile, apply infra tarball with protected path filtering
if [ "$PROFILE" = "infrastructure" ]; then
    echo ""
    echo "Applying infrastructure components..."
    apply_infra_tarball

    # Update infra version file
    INFRA_COMMIT=$(git ls-remote "https://github.com/$INFRA_REPO.git" "$INFRA_BRANCH" | cut -c1-7)
    DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    echo "{\"commit\": \"$INFRA_COMMIT\", \"updated\": \"$DATE\"}" > .infra-template-version
    echo "  ✓ .infra-template-version updated"
fi
```

### 5.5: Migration: old full hooks to delegation stubs

```bash
# Detect old full hooks (without "Delegation stub" marker) and replace with stubs
for hook in onCreate postCreate postStart postAttach updateContent; do
    hook_file=".devcontainer/hooks/lifecycle/${hook}.sh"
    if [ -f "$hook_file" ] && ! grep -q "Delegation stub" "$hook_file"; then
        src_stub="$DEVCONTAINER_EXTRACT_DIR/.devcontainer/hooks/lifecycle/${hook}.sh"
        if [ -f "$src_stub" ]; then
            cp -f "$src_stub" "$hook_file"
            chmod +x "$hook_file"
            echo "  Migrated ${hook}.sh to delegation stub"
        fi
    fi
done
```

### 5.6: Cleanup deprecated files & MCP migration

```bash
[ -f ".coderabbit.yaml" ] && rm -f ".coderabbit.yaml" && echo "  Removed deprecated .coderabbit.yaml"

# Migration: remove deprecated MCP servers from runtime mcp.json
if [ -f "$HOME/.claude/mcp.json" ] && command -v jq &>/dev/null; then
    for server in codacy taskmaster; do
        if jq -e ".mcpServers.$server" "$HOME/.claude/mcp.json" &>/dev/null; then
            jq "del(.mcpServers.$server)" "$HOME/.claude/mcp.json" > "$HOME/.claude/mcp.json.tmp" && \
                mv "$HOME/.claude/mcp.json.tmp" "$HOME/.claude/mcp.json"
            echo "  Removed deprecated $server MCP server"
        fi
    done
fi

# Migration: remove .taskmaster/ directory
[ -d ".taskmaster" ] && rm -rf ".taskmaster" && echo "  Removed deprecated .taskmaster/"
```

### 5.7: Update devcontainer version file

```bash
# Get commit SHA via git ls-remote (strip-components removes dir name)
DC_COMMIT=$(git ls-remote "https://github.com/$DEVCONTAINER_REPO.git" "$DEVCONTAINER_BRANCH" | cut -c1-7)
DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)

if [ "$CONTEXT" = "container" ]; then
    echo "{\"commit\": \"$DC_COMMIT\", \"updated\": \"$DATE\"}" > .devcontainer/.template-version
else
    echo "{\"commit\": \"$DC_COMMIT\", \"updated\": \"$DATE\"}" > "$UPDATE_TARGET/.template-version"
fi
echo "  ✓ .template-version updated ($DC_COMMIT)"
```

### 5.8: Cleanup temp directories

```bash
# Temp directories are cleaned up automatically by trap EXIT
# Registered via CLEANUP_DIRS during download phase
```

### 5.9: Consolidated report

**Output (devcontainer only):**

```
═══════════════════════════════════════════════
  ✓ DevContainer updated successfully
═══════════════════════════════════════════════

  Profile : devcontainer
  Method  : git tarball (1 API call)
  Source  : kodflow/devcontainer-template
  Version : def5678

  Updated components:
    ✓ hooks          (scripts)
    ✓ commands       (slash commands + sub-modules)
    ✓ agents         (agent definitions)
    ✓ lifecycle      (delegation stubs)
    ✓ image-hooks    (image-embedded hooks)
    ✓ shared-utils   (utils.sh)
    ✓ p10k           (powerlevel10k)
    ✓ settings       (settings.json)
    ✓ compose        (devcontainer service)
    ✓ grepai         (bge-m3 config)
    ✓ mcp-template   (mcp.json.tpl)
    ✓ mcp-fragments  (context7, ktn-linter)
    ✓ docs           (design patterns KB)
    ✓ templates      (project/docs templates)
    ✓ devcontainer   (feature refs)
    ✓ Dockerfile     (image FROM)
    ✓ vscode         (.vscode/settings.json)

═══════════════════════════════════════════════
```

**Output (infrastructure profile):**

```
═══════════════════════════════════════════════
  ✓ DevContainer updated successfully
═══════════════════════════════════════════════

  Profile : infrastructure
  Method  : git tarball (2 API calls)
  Sources :
    - kodflow/devcontainer-template (def5678)
    - kodflow/infrastructure-template (abc1234)

  DevContainer components:
    ✓ hooks, commands, agents, lifecycle
    ✓ image-hooks, shared-utils, p10k, settings
    ✓ compose, grepai

  Infrastructure components:
    ✓ modules/ (12 files)
    ✓ stacks/ (8 files)
    ✓ ansible/ (15 files)
    ✓ packer/ (4 files)
    ✓ ci/ (6 files)
    ✓ tests/ (9 files)
    Protected: 3 skipped (inventory/, terragrunt.hcl)

═══════════════════════════════════════════════
```
