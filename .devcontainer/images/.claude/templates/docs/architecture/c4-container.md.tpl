# Container Diagram (C4 Level 2)

Technology choices and responsibility distribution within **{{PROJECT_NAME}}**.

**Audience:** Software architects, dev teams, DevOps engineers.

## Container Diagram

```mermaid
C4Container
    title Container Diagram — {{PROJECT_NAME}}

    {{C4_CONTAINER_PERSONS}}

    System_Boundary(system, "{{PROJECT_NAME}}") {
        {{C4_CONTAINERS}}
    }

    {{C4_CONTAINER_EXTERNAL_SYSTEMS}}

    {{C4_CONTAINER_RELATIONSHIPS}}

    UpdateLayoutConfig($c4ShapeInRow="3", $c4BoundaryInRow="1")

    %% Color: applied per-element (C4 ignores Mermaid themes)
    {{C4_CONTAINER_STYLES}}
```

<!-- COLOR RULES:
  Apply UpdateElementStyle to EVERY element in the diagram:

  Person (reused from Level 1):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_PRIMARY_BG}}", $borderColor="{{COLOR_PRIMARY_BORDER}}")

  Container (internal):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_PRIMARY_BG}}", $borderColor="{{COLOR_PRIMARY_BORDER}}")

  ContainerDb:
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_DATA_BG}}", $borderColor="{{COLOR_DATA_BORDER}}")

  ContainerQueue:
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_ASYNC_BG}}", $borderColor="{{COLOR_ASYNC_BORDER}}")

  System_Ext:
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_EXTERNAL_BG}}", $borderColor="{{COLOR_EXTERNAL_BORDER}}")

  Relationships:
    UpdateRelStyle(from, to, $textColor="{{COLOR_TEXT}}", $lineColor="{{COLOR_EDGE}}")
-->

<!-- GENERATION RULES:
  Elements inside System_Boundary:
    - Container(alias, "Label", "Technology", "Description")
    - ContainerDb(alias, "Label", "Technology", "Description")
    - ContainerQueue(alias, "Label", "Technology", "Description")

  Elements outside boundary:
    - Person(alias, "Label", "Description") — reuse from Level 1
    - System_Ext(alias, "Label", "Description") — reuse from Level 1

  Relationships:
    - Rel(from, to, "Action verb", "Protocol/Format")
    - ALWAYS specify technology: "JSON/HTTPS", "JDBC", "AMQP", "gRPC"
    - Cross-link: Transport column links to transport.md#{anchor}

  Constraints:
    - Max 15 elements inside the boundary
    - If more, split into multiple focused diagrams
    - Every container has Technology specified
    - Integrate deployment details here (don't create separate deployment diagram)
-->

## Containers

| Container | Technology | Responsibility | Transport | Format |
|-----------|-----------|----------------|-----------|--------|
<!-- FOR EACH container -->
<!-- | Web App | React, TypeScript | Customer SPA | [HTTPS](../transport.md#httphttps) | [JSON](../transport.md#json) | -->
<!-- | API Gateway | Node.js, Express | Request routing | [HTTPS](../transport.md#httphttps) | [JSON](../transport.md#json) | -->
<!-- | Database | PostgreSQL 15 | Persistent storage | [TCP](../transport.md#tcp-raw) | SQL | -->

## Data Stores

| Store | Technology | Purpose | Access Pattern |
|-------|-----------|---------|----------------|
<!-- FOR EACH database/queue -->
<!-- | User DB | PostgreSQL | User profiles, credentials | Read-heavy | -->
<!-- | Message Queue | RabbitMQ | Async event processing | Write-heavy | -->
<!-- | Cache | Redis | Session data, hot cache | Read-heavy | -->

## Communication Map

| Source | Destination | Protocol | Format | Direction |
|--------|------------|----------|--------|-----------|
<!-- FOR EACH relationship between containers -->
<!-- | API Gateway | Auth Service | gRPC | Protobuf | Sync | -->
<!-- | Account Service | Message Queue | AMQP | JSON | Async | -->

---

*[← System Context](c4-context.md) | [Back to Overview](README.md) | [Next: Component Diagram →](c4-component.md)*
