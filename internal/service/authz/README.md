# authz

Package `authz` implements the authorization port declared in
`internal/core/authz`.

- `NewRBAC` — role-based grants over a table the caller supplies. It answers
  `Allow` or `Abstain` and **never** `Deny` for an ungranted request, which is
  what keeps it composable.
- `NewABAC` — attribute rules, each with an explicit `Allow` or `Deny` effect
  and a `Condition`. Explicit refusals come from here.
- `DenyOverrides` — the one combining algorithm. A refusal wins; an `Allow`
  never short-circuits, so the answer does not depend on the order.
- `Check` — the closure. The only place where "nobody said `Allow`" becomes a
  refusal, and it closes toward `Deny`.
- The built-in conditions (`AttrEquals`, `AttrIsTrue`, `AttrAtLeast`,
  `AttrContains`, `AttrMatchesSubject`) and the combinators (`Not`, `AllOf`,
  `AnyOf`). Every one of them reports an absent or wrong-kind attribute as an
  **error**, and an error is absorbing in every combinator — including `Not`,
  which propagates it instead of inverting it.

There is no policy language, no wildcard, no rule file format and no
relationship model: a condition is a Go func and a grant table is a Go slice.
The evaluation path allocates nothing — see `BENCH.md`. Facade: `pkg/v1/authz`.
ADR 0057. See `CLAUDE.md`.
