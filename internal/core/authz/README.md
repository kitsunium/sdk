# authz

Package `authz` declares the SDK's **authorization** port: `Policy` — may this
subject perform this action on this resource — the three-valued `Decision` it
answers with, the immutable `RequestValue` that carries the question, the typed
`AttrValue` facts attached to it, and the `Condition` an attribute rule is
written as.

`Policy` and `Condition` are function ports rather than interfaces, so neither
can grow a method and break a downstream implementer (ADR 0039).

Four things are decided here, and all four are security properties:

- **A `Decision` has three states.** `Allow`, `Deny` and `Abstain` — because an
  evaluator that has no opinion must be able to say so. Its zero value is
  `Abstain`, never `Allow`.
- **Nothing in this package turns an abstention into a grant.** The closure
  lives in `internal/service/authz.Check`, and it closes toward refusal.
- **An absent attribute is not a false one.** `RequestValue.Attr` returns
  `(AttrValue, bool)` and every `AttrValue` accessor reports a kind mismatch,
  so "the subject has no department" and "nobody said" stay different facts.
- **A refusal explains nothing to the caller.** All four sentinels carry the
  same wire-safe sentence; the diagnosis lives in `Private` and the fields.

It answers "may this", never "who is this" — identity comes from
`internal/core/session` or `internal/core/token`. There is no policy language,
no wildcard and no relationship model. The RBAC and ABAC evaluators, the
combiner and the built-in conditions live in `internal/service/authz`; facade:
`pkg/v1/authz`. ADR 0057. See `CLAUDE.md`.
