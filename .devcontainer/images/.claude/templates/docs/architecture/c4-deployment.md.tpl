# Deployment Diagram (C4 Infrastructure)

Infrastructure topology for **{{PROJECT_NAME}}** in production.

**Audience:** DevOps engineers, SREs, platform teams.

<!-- GENERATION RULES:
  Condition: Only generate if deployment signals detected:
    - docker-compose.yml with replicas
    - Kubernetes manifests (k8s/, manifests/, helm/)
    - Terraform/Terragrunt files
    - Load balancer configuration
    - Consensus code (Raft, Paxos)

  If no deployment signals, this page should NOT be generated.
-->

## Deployment Diagram

```mermaid
C4Deployment
    title Deployment — {{PROJECT_NAME}} (Production)

    {{C4_DEPLOYMENT_NODES}}

    {{C4_DEPLOYMENT_RELATIONSHIPS}}

    UpdateLayoutConfig($c4ShapeInRow="2", $c4BoundaryInRow="1")

    %% Color: applied per-element (C4 ignores Mermaid themes)
    {{C4_DEPLOYMENT_STYLES}}
```

<!-- COLOR RULES:
  Apply UpdateElementStyle to EVERY element in the diagram:

  Container (inside Deployment_Node):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_PRIMARY_BG}}", $borderColor="{{COLOR_PRIMARY_BORDER}}")

  ContainerDb (inside Deployment_Node):
    UpdateElementStyle(alias, $fontColor="{{COLOR_TEXT}}", $bgColor="{{COLOR_DATA_BG}}", $borderColor="{{COLOR_DATA_BORDER}}")

  Deployment_Node:
    Styled via CSS (.node .outer → fill:#2d2d2d, stroke:{{COLOR_EXTERNAL_BORDER}})
    No UpdateElementStyle needed — CSS handles it globally

  Relationships:
    UpdateRelStyle(from, to, $textColor="{{COLOR_TEXT}}", $lineColor="{{COLOR_EDGE}}")
-->

<!-- GENERATION RULES:
  Elements:
    - Deployment_Node(alias, "Label", "Type/Spec") { ... }
    - Nest Deployment_Nodes for hierarchy: Cloud → Cluster → Pod → Container
    - Place Container/ContainerDb elements inside deepest node

  Relationships:
    - Rel(from, to, "Communication", "Protocol")
    - Show inter-node communication (not intra-node)

  Constraints:
    - Max 3 nesting levels for Deployment_Node
    - Show only production environment (not dev/staging)
    - Include replica counts if known
-->

## Infrastructure

| Node | Type | Spec | Containers |
|------|------|------|------------|
<!-- FOR EACH deployment node -->
<!-- | EKS Cluster | Kubernetes | us-east-1, 3 nodes | API, Auth, Account | -->
<!-- | RDS Aurora | PostgreSQL | db.r6g.xlarge, 2 replicas | User DB, Account DB | -->
<!-- | ElastiCache | Redis | cache.r6g.large, 3 nodes | Session Cache | -->

## Scaling Strategy

| Aspect | Strategy | Details |
|--------|----------|---------|
<!-- | Compute | Horizontal | Auto-scale 2-10 pods based on CPU | -->
<!-- | Database | Read replicas | 1 primary + 2 read replicas | -->
<!-- | Cache | Cluster mode | 3 Redis nodes with sharding | -->

## Network

| Source | Destination | Port | Protocol | TLS |
|--------|------------|:----:|----------|:---:|
<!-- | Internet | Load Balancer | 443 | HTTPS | Yes | -->
<!-- | LB | API Pods | 8080 | HTTP | No (internal) | -->
<!-- | API Pods | Database | 5432 | PostgreSQL | Yes (mTLS) | -->

## Recommended Configuration

| Scenario | Nodes | CPU | RAM | Storage |
|----------|:-----:|:---:|:---:|:-------:|
<!-- | Minimum | 2 | 2 vCPU | 4 GB | 50 GB | -->
<!-- | Recommended | 3 | 4 vCPU | 8 GB | 100 GB | -->
<!-- | High Availability | 5 | 8 vCPU | 16 GB | 500 GB | -->

---

*[Back to Architecture Overview](README.md)*
