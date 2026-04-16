<!-- /docs-generated: {"date":"{{TIMESTAMP}}","commit":"{{LAST_COMMIT_SHA}}","pages":{{TOTAL_PAGES}},"agents":{{N}}} -->

<div class="hero" markdown>

# {{PROJECT_NAME}}

**{{PROJECT_TAGLINE}}**

[Get Started :material-arrow-right:](#how-it-works){ .md-button .md-button--primary }

</div>

---

<!-- IF INTERNAL_PROJECT == true: simple feature table -->
<!-- USE THIS VARIANT for internal projects -->
<!--
## Features

| Feature | Description |
|---------|-------------|
| **{{FEATURE_NAME}}** | {{FEATURE_DESCRIPTION}} |
-->

<!-- IF INTERNAL_PROJECT == false: competitive comparison table -->
<!-- USE THIS VARIANT for external projects -->
<!--
## Feature Comparison

| Feature | {{PROJECT_NAME}} :star: | {{COMPETITOR_A}} | {{COMPETITOR_B}} | {{COMPETITOR_C}} |
|---------|:-:|:-:|:-:|:-:|
| **{{FEATURE_NAME}}** | :white_check_mark: | :warning: | :x: | :x: |
| **Price** | Free | $$$ | Free | $$ |
{{IF_PUBLIC_REPO}}| **Open Source** | :white_check_mark: | :x: | :white_check_mark: | :x: |{{/IF_PUBLIC_REPO}}

> :white_check_mark: Full support | :warning: Partial | :x: Not available
-->

## How it works

<!-- COLOR RULES for OVERVIEW_DIAGRAM:
  This is a flowchart (NOT C4), so use %%{init}%% + classDef:

  1. Start with %%{init}%% block (from mermaid_color_directives.init_block)
  2. Include all 5 classDef declarations (primary, data, async, external, error)
  3. Assign semantic classes to nodes:
     - Internal components: :::primary
     - Data stores/databases: :::data
     - Queues/async processors: :::async
     - External services: :::external
     - Error/failure nodes: :::error
  4. Example:
     ```
     %%{init: {'theme': 'dark', 'themeVariables': {...}}}%%
     flowchart LR
       A[Web App]:::primary --> B[(Database)]:::data
       A --> C{{Queue}}:::async
       A --> D[External API]:::external
       classDef primary fill:{{COLOR_PRIMARY_BG}},stroke:{{COLOR_PRIMARY_BORDER}},color:{{COLOR_TEXT}}
       classDef data fill:{{COLOR_DATA_BG}},stroke:{{COLOR_DATA_BORDER}},color:{{COLOR_TEXT}}
       classDef async fill:{{COLOR_ASYNC_BG}},stroke:{{COLOR_ASYNC_BORDER}},color:{{COLOR_TEXT}}
       classDef external fill:{{COLOR_EXTERNAL_BG}},stroke:{{COLOR_EXTERNAL_BORDER}},color:{{COLOR_TEXT}}
       classDef error fill:{{COLOR_ERROR_BG}},stroke:{{COLOR_ERROR_BORDER}},color:{{COLOR_TEXT}}
     ```
-->

```mermaid
{{OVERVIEW_DIAGRAM}}
```

{{OVERVIEW_EXPLANATION}}

## Quick Start

{{QUICK_START_STEPS}}

---

*{{PROJECT_NAME}} · {{LICENSE}}{{IF_PUBLIC_REPO}} · [:material-github: GitHub]({{GIT_REMOTE_URL}}){{/IF_PUBLIC_REPO}}*
