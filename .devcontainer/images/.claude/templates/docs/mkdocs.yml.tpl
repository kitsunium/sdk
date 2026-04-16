site_name: "{{PROJECT_NAME}} Documentation"
site_description: "{{GENERATED_DESCRIPTION}}"
site_url: ""
docs_dir: docs
site_dir: site

# --- CONDITIONAL: only if PUBLIC_REPO == true ---
# repo_url: "{{GIT_REMOTE_URL}}"
# repo_name: "{{REPO_NAME}}"
# edit_uri: "edit/main/docs/"
# --- END CONDITIONAL ---

theme:
  name: material
  palette:
    scheme: slate
    primary: custom
    accent: custom
  features:
    - navigation.tabs
    - navigation.sections
    - navigation.expand
    - navigation.top
    - search.suggest
    - search.highlight
    - content.code.copy
    - content.tabs.link
  # --- CONDITIONAL: only if PUBLIC_REPO == true ---
  # icon:
  #   repo: fontawesome/brands/github
  # --- END CONDITIONAL ---

plugins:
  - search

markdown_extensions:
  - pymdownx.highlight:
      anchor_linenums: true
      line_spans: __span
      pygments_lang_class: true
  - pymdownx.inlinehilite
  - pymdownx.snippets
  - pymdownx.superfences:
      custom_fences:
        - name: mermaid
          class: mermaid
          format: !!python/name:pymdownx.superfences.fence_code_format
  - pymdownx.tabbed:
      alternate_style: true
  - pymdownx.details
  - pymdownx.emoji:
      emoji_index: !!python/name:material.extensions.emoji.twemoji
      emoji_generator: !!python/name:material.extensions.emoji.to_svg
  - admonition
  - attr_list
  - md_in_html
  - tables
  - toc:
      permalink: true

# --- NAVIGATION ---
# Generated dynamically by /docs Phase 3 based on:
#   - PROJECT_TYPE (template/library/application)
#   - PUBLIC_REPO (controls GitHub tab)
#   - API_COUNT (controls API tab: 0=hidden, 1=direct, N=dropdown)
#   - Scoring results (which sections are primary/standard/reference)
#
# nav_algorithm:
#   1. "Docs" tab: index.md + scored sections from Phase 3
#   2. "Transport" tab: transport.md (always present)
#   3. API tab (conditional):
#      - API_COUNT == 0 → no nav item
#      - API_COUNT == 1 → "API: api/overview.md"
#      - API_COUNT > 1  → "APIs:" dropdown with Overview + per-API pages
#   4. "Changelog" tab: changelog.md (always present)
#   5. "GitHub" tab (conditional): external link, only if PUBLIC_REPO == true
#   6. Validate: every nav entry points to an existing file
#
# Example output (public, external, 2 APIs):
nav:
  - Docs:
    - Home: index.md
    # ... scored sections inserted by Phase 3 ...
  - Transport: transport.md
  # --- CONDITIONAL: API_COUNT == 1 ---
  # - API: api/overview.md
  # --- CONDITIONAL: API_COUNT > 1 ---
  # - APIs:
  #   - Overview: api/overview.md
  #   - "{{API_NAME}}": api/{{API_SLUG}}.md
  # --- END CONDITIONAL ---
  - Changelog: changelog.md
  # --- CONDITIONAL: only if PUBLIC_REPO == true ---
  # - GitHub: {{GIT_REMOTE_URL}}
  # --- END CONDITIONAL ---

# --- CONDITIONAL: only if PUBLIC_REPO == true ---
# extra:
#   social:
#     - icon: fontawesome/brands/github
#       link: "{{GIT_REMOTE_URL}}"
# --- END CONDITIONAL ---

extra_css:
  - stylesheets/theme.css

extra:
  generator: false

# --- CONDITIONAL copyright ---
# If PUBLIC_REPO == true:
#   copyright: "{{PROJECT_NAME}} · {{LICENSE}} · <a href='{{GIT_REMOTE_URL}}'>GitHub</a>"
# If PUBLIC_REPO == false:
#   copyright: "{{PROJECT_NAME}} · {{LICENSE}}"
