# Component Diagram (C4 Level 3)

Internal structure of key containers in **{{PROJECT_NAME}}**.

**Audience:** Developers working on specific containers.

<!-- GENERATION RULES:
  Condition: Only generate if container has:
    - >5 significant internal modules
    - Multiple teams maintaining the container
    - Complex internal logic not obvious from code

  If no container qualifies, this page should contain a note:
    "All containers have straightforward internal structures.
     Refer to source code for implementation details."
-->

<!-- FOR EACH qualifying container, generate a section: -->

## {{CONTAINER_NAME}}

**Technology:** {{CONTAINER_TECH}}
**Responsibility:** {{CONTAINER_DESCRIPTION}}

```mermaid
C4Component
    title Component Diagram — {{CONTAINER_NAME}}

    {{C4_COMPONENT_EXTERNAL_CONTAINERS}}

    Container_Boundary(container, "{{CONTAINER_NAME}}") {
        {{C4_COMPONENTS}}
    }

    {{C4_COMPONENT_EXTERNAL_STORES}}

    {{C4_COMPONENT_RELATIONSHIPS}}

    %% Color: applied per-element (C4 ignores Mermaid themes)
    {{C4_COMPONENT_STYLES}}
```

<!-- COLOR RULES:
  Apply UpdateElementStyle to EVERY element in the diagram:

  Component (internal):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_PRIMARY_BG}}", $borderColor="{{COLOR_PRIMARY_BORDER}}")

  ComponentDb:
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_DATA_BG}}", $borderColor="{{COLOR_DATA_BORDER}}")

  ComponentQueue:
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_ASYNC_BG}}", $borderColor="{{COLOR_ASYNC_BORDER}}")

  Container (adjacent, outside boundary):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_PRIMARY_BG}}", $borderColor="{{COLOR_PRIMARY_BORDER}}")

  ContainerDb (outside boundary):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_DATA_BG}}", $borderColor="{{COLOR_DATA_BORDER}}")

  Relationships:
    UpdateRelStyle(from, to, $textColor="{{COLOR_TEXT}}", $lineColor="{{COLOR_EDGE}}")
-->

<!-- GENERATION RULES:
  Elements inside Container_Boundary:
    - Component(alias, "Label", "Technology", "Description")
    - ComponentDb(alias, "Label", "Technology", "Description")
    - ComponentQueue(alias, "Label", "Technology", "Description")

  Elements outside boundary:
    - Container(alias, "Label", "Tech", "Desc") — adjacent containers
    - ContainerDb(alias, "Label", "Tech", "Desc") — databases

  Constraints:
    - Max 12 components per container diagram
    - If more, split by bounded context or feature area
    - Focus on what's hard to discover from code alone
-->

### Components

| Component | Technology | Responsibility | Key Files |
|-----------|-----------|----------------|-----------|
<!-- FOR EACH component -->
<!-- | Auth Controller | Spring MVC | REST endpoints for auth | src/auth/controller.go | -->
<!-- | Token Service | Spring Bean | JWT creation/validation | src/auth/token.go | -->

### Design Patterns

| Pattern | Where | Why |
|---------|-------|-----|
<!-- FOR EACH detected pattern in this container -->
<!-- | Repository | UserRepository | Data access abstraction | -->
<!-- | Factory | TokenFactory | Multiple token types | -->

---

*[← Container Diagram](c4-container.md) | [Back to Overview](README.md)*
