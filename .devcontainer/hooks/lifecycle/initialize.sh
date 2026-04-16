#!/bin/bash
# ============================================================================
# initialize.sh - Runs on HOST machine BEFORE container build
# ============================================================================
# This script runs on the host machine before Docker Compose starts.
# Use it for: .env setup, project name configuration, feature validation.
# ============================================================================

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HOOKS_DIR="$(dirname "$SCRIPT_DIR")"
DEVCONTAINER_DIR="$(dirname "$HOOKS_DIR")"
ENV_FILE="$DEVCONTAINER_DIR/.env"

# Extract project name from git remote URL (with fallback if no remote)
REPO_NAME=$(basename "$(git config --get remote.origin.url 2>/dev/null || echo "devcontainer")" .git)

# Sanitize project name for Docker Compose requirements:
# - Must start with a letter or number
# - Only lowercase alphanumeric, hyphens, and underscores allowed
REPO_NAME=$(echo "$REPO_NAME" | sed 's/^[^a-zA-Z0-9]*//' | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9_-]/-/g')

# If name is empty after sanitization, use a default
if [ -z "$REPO_NAME" ]; then
    REPO_NAME="devcontainer"
fi

echo "Initializing devcontainer environment..."
echo "Project name: $REPO_NAME"

# If .env doesn't exist, create it from .env.example
if [ ! -f "$ENV_FILE" ]; then
    echo "Creating .env from .env.example..."
    cp "$HOOKS_DIR/shared/.env.example" "$ENV_FILE"
fi

# Update or add COMPOSE_PROJECT_NAME in .env
if grep -q "^COMPOSE_PROJECT_NAME=" "$ENV_FILE"; then
    # Update existing line
    if [[ "$OSTYPE" == "darwin"* ]]; then
        # macOS
        sed -i '' "s|^COMPOSE_PROJECT_NAME=.*|COMPOSE_PROJECT_NAME=$REPO_NAME|" "$ENV_FILE"
    else
        # Linux
        sed -i "s|^COMPOSE_PROJECT_NAME=.*|COMPOSE_PROJECT_NAME=$REPO_NAME|" "$ENV_FILE"
    fi
    echo "Updated COMPOSE_PROJECT_NAME=$REPO_NAME in .env"
else
    # Add at the beginning of the file
    echo "COMPOSE_PROJECT_NAME=$REPO_NAME" | cat - "$ENV_FILE" > "$ENV_FILE.tmp" && mv "$ENV_FILE.tmp" "$ENV_FILE"
    echo "Added COMPOSE_PROJECT_NAME=$REPO_NAME to .env"
fi

# ============================================================================
# Validate devcontainer features
# ============================================================================
echo ""
echo "Validating devcontainer features..."

FEATURES_DIR="$DEVCONTAINER_DIR/features"
ERRORS=0
FIXED=0

for category in "$FEATURES_DIR"/*; do
    [ ! -d "$category" ] && continue

    for feature in "$category"/*; do
        [ ! -d "$feature" ] && continue

        # Skip utility directories (not actual features)
        [ "$(basename "$feature")" = "shared" ] && continue

        feature_name="$(basename "$category")/$(basename "$feature")"

        # Check devcontainer-feature.json
        if [ ! -f "$feature/devcontainer-feature.json" ]; then
            echo "ERROR: $feature_name: Missing devcontainer-feature.json"
            ERRORS=$((ERRORS + 1))
            continue
        fi

        # Check install.sh
        if [ ! -f "$feature/install.sh" ]; then
            echo "ERROR: $feature_name: Missing install.sh"
            ERRORS=$((ERRORS + 1))
            continue
        fi

        # Fix permissions if needed
        if [ ! -x "$feature/install.sh" ]; then
            chmod +x "$feature/install.sh"
            FIXED=$((FIXED + 1))
        fi
    done
done

if [ $ERRORS -gt 0 ]; then
    echo ""
    echo "ERROR: Found $ERRORS critical error(s) in features!"
    echo "Please fix missing files before building the devcontainer."
    exit 1
fi

if [ $FIXED -gt 0 ]; then
    echo "Fixed permissions on $FIXED install.sh file(s)"
fi

echo "All features validated successfully"

# ============================================================================
# Ollama Installation (Host GPU Acceleration for grepai)
# ============================================================================
# Ollama runs on the HOST to leverage GPU (Metal on Mac, CUDA on Linux)
# The DevContainer connects via host.docker.internal:11434
# ============================================================================
# Extract Ollama model from grepai config (single source of truth)
GREPAI_CONFIG="$DEVCONTAINER_DIR/images/grepai.config.yaml"
OLLAMA_MODEL=$(grep -E '^\s+model:' "$GREPAI_CONFIG" 2>/dev/null | awk '{print $2}' | head -1)
OLLAMA_MODEL="${OLLAMA_MODEL:-bge-m3}"

echo ""
echo "Setting up Ollama for GPU-accelerated semantic search..."

# Detect OS
detect_os() {
    case "$OSTYPE" in
        darwin*)  echo "macos" ;;
        linux*)   echo "linux" ;;
        msys*|cygwin*|mingw*) echo "windows" ;;
        *)        echo "unknown" ;;
    esac
}

# Check if Ollama is installed
check_ollama_installed() {
    command -v ollama &>/dev/null
}

# Check if Ollama is running
check_ollama_running() {
    curl -sf --connect-timeout 2 http://localhost:11434/api/tags &>/dev/null
}

# Install Ollama based on OS
install_ollama() {
    local os="$1"
    echo "Installing Ollama..."

    case "$os" in
        macos)
            if command -v brew &>/dev/null; then
                brew install ollama
            else
                curl -fsSL https://ollama.com/install.sh | sh
            fi
            ;;
        linux)
            curl -fsSL https://ollama.com/install.sh | sh
            ;;
        windows)
            echo "Windows detected. Please install Ollama manually:"
            echo "  Download from: https://ollama.com/download/windows"
            echo "  Or via winget: winget install Ollama.Ollama"
            return 1
            ;;
        *)
            echo "Unknown OS. Please install Ollama manually from https://ollama.com"
            return 1
            ;;
    esac
}

# Ensure Ollama binds on all interfaces so Docker containers can reach it
# via host.docker.internal:11434 (default is 127.0.0.1 = container-invisible)
ensure_ollama_host_binding() {
    local os="$1"

    case "$os" in
        macos)
            # Set for current session + persist via launchctl
            launchctl setenv OLLAMA_HOST 0.0.0.0 2>/dev/null || true
            export OLLAMA_HOST=0.0.0.0
            ;;
        linux)
            export OLLAMA_HOST=0.0.0.0
            # Persist in systemd override if service exists (sudo -n = non-interactive)
            if systemctl list-unit-files 2>/dev/null | grep -q "ollama"; then
                if sudo -n true 2>/dev/null; then
                    sudo -n mkdir -p /etc/systemd/system/ollama.service.d 2>/dev/null || true
                    echo -e "[Service]\nEnvironment=OLLAMA_HOST=0.0.0.0" | \
                        sudo -n tee /etc/systemd/system/ollama.service.d/bind-all.conf >/dev/null 2>&1 || true
                    sudo -n systemctl daemon-reload 2>/dev/null || true
                fi
            fi
            ;;
    esac
}

# Start Ollama daemon
start_ollama() {
    local os="$1"
    echo "Starting Ollama daemon..."

    # Bind on 0.0.0.0 so containers can reach via host.docker.internal
    ensure_ollama_host_binding "$os"

    case "$os" in
        macos)
            # On macOS, try brew services first, then launchctl, then nohup
            if command -v brew &>/dev/null && brew list ollama &>/dev/null; then
                if ! OLLAMA_HOST=0.0.0.0 brew services start ollama 2>/dev/null; then
                    # brew services failed — try launchctl
                    if launchctl list 2>/dev/null | grep -q "com.ollama"; then
                        launchctl start com.ollama.ollama >/dev/null 2>&1 || \
                            OLLAMA_HOST=0.0.0.0 nohup ollama serve >/dev/null 2>&1 &
                    else
                        OLLAMA_HOST=0.0.0.0 nohup ollama serve >/dev/null 2>&1 &
                    fi
                fi
            elif launchctl list 2>/dev/null | grep -q "com.ollama"; then
                launchctl start com.ollama.ollama >/dev/null 2>&1 || \
                    OLLAMA_HOST=0.0.0.0 nohup ollama serve >/dev/null 2>&1 &
            else
                OLLAMA_HOST=0.0.0.0 nohup ollama serve >/dev/null 2>&1 &
            fi
            ;;
        linux)
            # Prefer systemd with non-interactive sudo; fall back to nohup
            if systemctl list-unit-files 2>/dev/null | grep -q "ollama" && sudo -n true 2>/dev/null; then
                sudo -n systemctl enable ollama 2>/dev/null || true
                sudo -n systemctl restart ollama 2>/dev/null || OLLAMA_HOST=0.0.0.0 nohup ollama serve >/dev/null 2>&1 &
            else
                OLLAMA_HOST=0.0.0.0 nohup ollama serve >/dev/null 2>&1 &
            fi
            ;;
        windows)
            # On Windows, Ollama typically runs as a service after installation
            echo "Please ensure Ollama is running (check system tray)"
            echo "Set OLLAMA_HOST=0.0.0.0 in system environment for container access"
            ;;
    esac

    # Wait for Ollama to be ready (max 30 seconds)
    local retries=15
    while [ $retries -gt 0 ]; do
        if check_ollama_running; then
            echo "Ollama is ready (listening on 0.0.0.0:11434)"
            return 0
        fi
        retries=$((retries - 1))
        sleep 2
    done

    echo "Warning: Ollama did not start in time"
    return 1
}

# Pull/update embedding model (idempotent — ollama pull checks digest)
pull_model() {
    local model="$1"
    echo "Pulling/updating embedding model: $model..."
    ollama pull "$model"
}

# Check if the embedding model is already pulled (exact name match on first column)
check_model_pulled() {
    local model="$1"
    # Use fixed-string matching to avoid regex metacharacter issues in model names
    ollama list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qxF "$model" ||
    ollama list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qxF "${model}:latest"
}

# Ensure Ollama is registered as a persistent service (survives reboots)
ensure_ollama_persistent() {
    local os="$1"
    case "$os" in
        macos)
            if command -v brew &>/dev/null && brew list ollama &>/dev/null; then
                # brew services auto-starts on boot
                if ! brew services list 2>/dev/null | grep ollama | grep -q started; then
                    echo "  Registering Ollama as persistent brew service..."
                    if ! OLLAMA_HOST=0.0.0.0 brew services start ollama 2>/dev/null; then
                        echo "  Warning: brew services start failed — Ollama may not persist across reboots"
                    fi
                else
                    echo "  Ollama already registered as persistent brew service"
                fi
            fi
            ;;
        linux)
            if systemctl list-unit-files 2>/dev/null | grep -q "ollama"; then
                if [ "$(systemctl is-enabled ollama 2>/dev/null || true)" != "enabled" ]; then
                    if sudo -n true 2>/dev/null; then
                        echo "  Enabling Ollama systemd service (persist across reboots)..."
                        sudo -n systemctl enable ollama 2>/dev/null || true
                    else
                        echo "  SKIP: sudo not available (run manually: sudo systemctl enable ollama)"
                    fi
                else
                    echo "  Ollama systemd service already enabled"
                fi
            fi
            ;;
    esac
}

# ============================================================================
# Main Ollama setup flow — each step checks and fixes if needed
# ============================================================================
OS=$(detect_os)
echo "Detected OS: $OS"

# Step 1: Ensure Ollama is installed
echo ""
echo "[1/5] Checking Ollama installation..."
if check_ollama_installed; then
    echo "  OK: Ollama is installed ($(ollama --version 2>/dev/null || echo 'version unknown'))"
else
    echo "  MISSING: Installing Ollama..."
    if ! install_ollama "$OS"; then
        echo "  FAILED: Could not install Ollama automatically"
        echo "  Semantic search (grepai) will be unavailable"
        echo "  Manual install: https://ollama.com/download"
    fi
fi

# Step 2: Ensure 0.0.0.0 binding (container-accessible)
echo ""
echo "[2/5] Checking host binding (0.0.0.0 for container access)..."
if check_ollama_installed; then
    ensure_ollama_host_binding "$OS"
    echo "  OK: OLLAMA_HOST=0.0.0.0 configured"
else
    echo "  SKIP: Ollama not installed"
fi

# Step 3: Ensure Ollama is running
echo ""
echo "[3/5] Checking Ollama service..."
if check_ollama_installed; then
    if check_ollama_running; then
        echo "  OK: Ollama is running (port 11434)"
    else
        echo "  DOWN: Starting Ollama..."
        start_ollama "$OS" || true
        if check_ollama_running; then
            echo "  OK: Ollama started successfully"
        else
            echo "  FAILED: Ollama did not start — grepai will be unavailable"
        fi
    fi
else
    echo "  SKIP: Ollama not installed"
fi

# Step 4: Ensure Ollama persists across reboots
echo ""
echo "[4/5] Checking service persistence (survive reboots)..."
if check_ollama_installed; then
    ensure_ollama_persistent "$OS"
else
    echo "  SKIP: Ollama not installed"
fi

# Step 5: Ensure embedding model is pulled
echo ""
echo "[5/5] Checking embedding model ($OLLAMA_MODEL)..."
if check_ollama_installed && check_ollama_running; then
    if check_model_pulled "$OLLAMA_MODEL"; then
        echo "  OK: Model $OLLAMA_MODEL already available"
    else
        echo "  MISSING: Pulling $OLLAMA_MODEL..."
        pull_model "$OLLAMA_MODEL" || true
        if check_model_pulled "$OLLAMA_MODEL"; then
            echo "  OK: Model $OLLAMA_MODEL pulled successfully"
        else
            echo "  FAILED: Could not pull $OLLAMA_MODEL"
        fi
    fi
else
    echo "  SKIP: Ollama not running"
fi

# Summary
echo ""
if check_ollama_installed && check_ollama_running && check_model_pulled "$OLLAMA_MODEL"; then
    echo "Ollama setup complete — GPU-accelerated semantic search ready"
else
    echo "Warning: Ollama setup incomplete — grepai semantic search may be unavailable"
    if ! check_ollama_installed; then
        echo "  Install: https://ollama.com/download"
    elif ! check_ollama_running; then
        echo "  Start: ollama serve"
    elif ! check_model_pulled "$OLLAMA_MODEL"; then
        echo "  Pull model: ollama pull $OLLAMA_MODEL"
    fi
fi

# ============================================================================
# Pull latest Docker image (bypass Docker cache on rebuild)
# ============================================================================
echo ""
echo "Pulling latest devcontainer image..."
docker pull ghcr.io/kodflow/devcontainer-template:latest 2>/dev/null || echo "Warning: Could not pull latest image, using cached version"

# ============================================================================
# Clean up existing containers to prevent race conditions during rebuild
# ============================================================================
echo ""
echo "Cleaning up existing devcontainer instances..."
docker compose -f "$DEVCONTAINER_DIR/docker-compose.yml" --project-name "$REPO_NAME" down --remove-orphans --timeout 5 2>/dev/null || true
echo "Cleanup complete"

echo ""
echo "Environment initialization complete!"
