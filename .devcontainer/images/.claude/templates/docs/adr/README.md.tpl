# Architecture Decision Records

This section documents key architectural decisions for **{{PROJECT_NAME}}**.

## What is an ADR?

An Architecture Decision Record (ADR) captures an important architectural decision made along with its context and consequences.

## ADR Template

We use the [MADR](https://adr.github.io/madr/) (Markdown Any Decision Records) format:

```markdown
# ADR-NNNN: Title

## Status
Proposed | Accepted | Deprecated | Superseded by ADR-XXXX

## Context
What is the issue that we're seeing that is motivating this decision?

## Decision
What is the change that we're proposing and/or doing?

## Consequences
What becomes easier or more difficult to do because of this change?
```

## Decisions

| ADR | Title | Status | Date |
|-----|-------|--------|------|
| [0001](0001-record-architecture-decisions.md) | Record Architecture Decisions | Accepted | {{GENERATED_DATE}} |

## Creating a New ADR

1. Copy the template from `0001-template.md`
2. Rename to `NNNN-short-title.md` (next sequential number)
3. Fill in all sections
4. Update this README with the new ADR
5. Submit for review

---

*ADR format based on [MADR](https://adr.github.io/madr/)*
