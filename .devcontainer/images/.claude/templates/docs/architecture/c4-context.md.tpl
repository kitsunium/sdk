# System Context (C4 Level 1)

High-level view of **{{PROJECT_NAME}}** and its interactions with users and external systems.

**Audience:** Everyone (technical and non-technical stakeholders).

## Context Diagram

```mermaid
C4Context
    title System Context — {{PROJECT_NAME}}

    {{C4_CONTEXT_PERSONS}}
    {{C4_CONTEXT_SYSTEMS}}
    {{C4_CONTEXT_EXTERNAL_SYSTEMS}}

    {{C4_CONTEXT_RELATIONSHIPS}}

    UpdateLayoutConfig($c4ShapeInRow="3", $c4BoundaryInRow="1")

    %% Color: applied per-element (C4 ignores Mermaid themes)
    {{C4_CONTEXT_STYLES}}
```

<!-- COLOR RULES:
  Apply UpdateElementStyle to EVERY element in the diagram:

  Person/System (internal):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_PRIMARY_BG}}", $borderColor="{{COLOR_PRIMARY_BORDER}}")

  System_Ext/Person_Ext/SystemDb_Ext/SystemQueue_Ext:
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_EXTERNAL_BG}}", $borderColor="{{COLOR_EXTERNAL_BORDER}}")

  Relationships:
    UpdateRelStyle(from, to, $textColor="{{COLOR_TEXT}}", $lineColor="{{COLOR_EDGE}}")
-->

<!-- GENERATION RULES:
  Elements:
    - Person(alias, "Label", "Description") for each user/actor
    - Person_Ext(alias, "Label", "Description") for external actors
    - System(alias, "{{PROJECT_NAME}}", "Description") for THE system (always exactly one)
    - System_Ext(alias, "Label", "Description") for each external dependency
    - SystemDb_Ext(alias, "Label", "Description") for external databases
    - SystemQueue_Ext(alias, "Label", "Description") for external queues

  Relationships:
    - Rel(from, to, "Action verb", "Protocol/Format")
    - ALWAYS include protocol: "REST/JSON", "gRPC/Protobuf", "JDBC", "SMTP"
    - Use action verbs: "Sends orders to", "Reads data from", "Authenticates with"

  Constraints:
    - Max 15 elements total (persons + systems + externals)
    - Exactly ONE internal system (the project)
    - Every relationship has a protocol label
    - Don't model users if connection points are obvious
-->

## Key Interactions

| From | To | Protocol | Purpose |
|------|----|----------|---------|
<!-- FOR EACH relationship in the diagram -->
<!-- | Customer | {{PROJECT_NAME}} | HTTPS | Access banking features | -->
<!-- | {{PROJECT_NAME}} | Email Service | SMTP | Send notifications | -->

## External Dependencies

| System | Type | Purpose | Criticality |
|--------|------|---------|:-----------:|
<!-- FOR EACH external system -->
<!-- | Payment Gateway | SaaS | Process card payments | High | -->
<!-- | Email Service | SaaS | Transactional emails | Medium | -->

---

*[Back to Architecture Overview](README.md) | [Next: Container Diagram →](c4-container.md)*
