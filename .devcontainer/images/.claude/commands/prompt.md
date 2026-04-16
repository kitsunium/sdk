---
name: prompt
description: |
  Generate the ideal prompt structure for /plan requests.
  Displays a template with placeholders, a filled example,
  and anti-patterns to help write clear, precise descriptions.
allowed-tools: []
---

# /prompt - Write Better /plan Descriptions

$ARGUMENTS

## Template

Copy this structure and fill in each line:

```
WHAT:  <action verb> <specific object> <details>
WHY:   <problem being solved or motivation>
WHERE: <files, modules, directories, or layers affected>
HOW:   <constraints, patterns to follow, things to avoid>
DONE:  <measurable success criteria>
```

**Target: 5-10 lines. One idea per line. Every line earns its place.**

---

## Filled Example

```
/plan "
WHAT:  Add rate limiting middleware to all public API endpoints
WHY:   Production users report 429 errors from upstream; we have no throttling
WHERE: src/middleware/, src/routes/public/, tests/middleware/
HOW:   Use token-bucket algorithm, follow existing middleware pattern in authMiddleware.ts, no external dependencies
DONE:  All public routes throttled at 100 req/min per IP, existing tests pass, new unit + integration tests added
"
```

**Why this works:** /plan can immediately Peek for the right files, Decompose into sub-tasks (middleware, route wiring, tests), pick the right patterns, and validate against concrete criteria.

---

## Interview Mode (Complex Features)

When requirements are unclear or the feature is large, skip the template and ask Claude to challenge your thinking:

```
/plan "
INTERVIEW: <brief description of what you want to build>
"
```

Claude will:
1. Ask targeted questions about implementation, edge cases, and tradeoffs
2. Challenge assumptions you might not have considered
3. Generate a spec from your answers before planning

**When to use Interview vs Template:**

| Situation | Use |
|-----------|-----|
| Clear scope, known files | Template (WHAT/WHY/WHERE/HOW/DONE) |
| Fuzzy requirements, many unknowns | Interview mode |
| Large feature (10+ files) | Interview mode |
| Bug fix with known location | Template |

---

## Dimension Guide

| Dimension | Question it answers | Feeds /plan phase |
|-----------|--------------------|--------------------|
| **WHAT** | What exactly to build/fix/change? | Peek (keywords), Decompose (objectives) |
| **WHY** | Why does this matter? What breaks without it? | Decompose (priority), Risks |
| **WHERE** | Which files/modules/layers are involved? | Parallelize (domain routing) |
| **HOW** | What constraints, patterns, or boundaries apply? | Pattern Consultation, Prerequisites |
| **DONE** | How do we know it works? (measurable) | Validation, Testing Strategy |

---

## Anti-Patterns

| Bad | Problem | Better |
|-----|---------|--------|
| `"Improve the API"` | No scope, no criteria | `"Add pagination to GET /users with cursor-based strategy"` |
| `"Make it faster"` | No metric, no target | `"Reduce /dashboard P95 latency from 800ms to 200ms"` |
| `"Refactor everything"` | Unbounded scope | `"Extract auth logic from controllers into src/services/auth.ts"` |
| `"Fix the bug"` | No symptom, no location | `"Fix: login returns 500 after session timeout. See src/auth/refresh.ts"` |
| 30-line wall of text | Claude loses focus | Split into /search (context) + /prompt (plan input) |

---

## When to Use Each Command

```
Need research first?  ->  /search <topic>  ->  generates .claude/contexts/{slug}.md
Ready to plan?        ->  /prompt           ->  helps write the description
Have the description? ->  /plan "..."       ->  creates plan + persists to .claude/plans/{slug}.md
Plan approved?        ->  /do               ->  executes the plan (from conversation or disk)
```

**Rule of thumb:** If your /plan description exceeds 10 lines, run /search first to offload context into `.claude/contexts/{slug}.md`, then keep the /plan description focused on WHAT/WHY/WHERE/HOW/DONE.

---

## Quick Reference

```
/plan "
WHAT:  [verb] [object] [specifics]
WHY:   [problem / motivation]
WHERE: [scope: files, modules, layers]
HOW:   [constraints, patterns, boundaries]
DONE:  [measurable success criteria]
"
```
