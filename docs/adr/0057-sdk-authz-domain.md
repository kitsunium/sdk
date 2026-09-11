# ADR 0057 — authorization domain (`authz`): abstention is a verdict, and there is no policy language

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Amends**: `internal/core/CLAUDE.md` §Purpose — a 24th core sibling
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (zero values are never inert), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (published ports), [ADR 0041](0041-sdk-scheduler-domain.md) (the FUNC-port precedent), [ADR 0046](0046-sdk-validation-domain.md) (a message names the rule and the bound, never the value), [ADR 0042](0042-sdk-token-domain.md) / [ADR 0045](0045-sdk-session-domain.md) (where a subject comes from), [ADR 0026](0026-sdk-resilience-domain.md) (no-registry precedent)

## Context

The SDK can now say who a caller is — `token` for a self-contained signed claim
set, `session` for a server-side revocable one — and has no way to say what
that caller may do. Every consumer therefore writes the same `if` ladder in the
same handler, and the two failure modes of a hand-rolled ladder are not
symmetric: the one that refuses too much is reported within the hour by the
user it blocked, and the one that permits too much is reported by whoever
exploits it.

The industry's answer to this is a policy engine, and every general-purpose
one converges on the same shape: a string grammar for conditions, a matcher
syntax for resources, a file format for the rule set, and a parser for all
three. That shape is what this ADR is mostly about refusing.

There is a second thing to get right, and it is the one that actually decides
whether the domain composes. **A policy that only knows about part of the
system must be able to say so.** A grant table for `orders` has nothing to
contribute to a request about a profile. If "nothing to contribute" has no
representation, it has to be spelled as one of the two real verdicts, and both
spellings are broken:

- Spelled **permit**, a policy authorizes every request it does not recognise.
  That is the hole, and it is silent.
- Spelled **refuse**, a policy vetoes every request the *other* policies exist
  to grant, because refusal is absorbing. That is the outage — and the usual
  repair for it is to make the combiner permissive, which puts the hole back
  one layer up.

Three states cost one enum value and remove both.

## Decision

**Add `internal/core/authz` as the 24th core sibling** (the port, the domain
values, the typed sentinels — block `0.2.26.*`), **and `internal/service/authz`
above it** (the RBAC and ABAC evaluators, the combiner, the closure and the
built-in conditions — block `0.3.56.*`), with `pkg/v1/authz` as the alias
facade. Standard four-layer shape (ADR 0001); no kernel primitive is added.

### D1 — There is no policy language, and that is the decision

A `Condition` is an ordinary Go func. A grant table is a Go slice. A resource
is a string compared by byte equality. There is **no expression parser, no
string grammar, no rule file format, no matcher syntax and no wildcard** —
anywhere in the domain. If a future reader finds one, it is a regression to
delete, not a feature to extend.

The argument is not taste, it is three concrete costs a DSL charges and a Go
value does not:

1. **A second, weaker language inside a program that already has one.** The
   host language has comparison, negation, arithmetic, short-circuits, a type
   checker and a debugger. A policy grammar starts with none of them and
   acquires them one release at a time, badly. `AllOf(a, Not(b))` needs no
   precedence table.
2. **A parser on the authorization path.** A parser is an attack surface, and
   this is the one place in a program where an attack surface is worst placed.
   A rule that is *mis-parsed* rather than rejected produces a policy that
   looks configured and is not.
3. **It is not typed.** `department == 9` and `department == "9"` are the same
   token stream to a grammar and two different comparisons here — see D7.

What a DSL is usually bought for is deployment: changing a rule without
recompiling. That property is available without the grammar. A caller loads
their own rule data through `internal/service/config`, validates it with
`internal/service/validation`, and builds `RuleValue`s from it — which keeps
the parsing, and the vocabulary it parses, in the application that owns them.

This follows the maintainer doctrine the SDK is built on: **low level, total
mastery, minimal dependencies.** A third-party policy engine — OPA/Rego, Cedar,
Casbin — would not be a mechanism of this SDK; it would be a *connector* to
someone else's evaluator, with its own version skew, its own CVE feed and its
own semantics for the exact questions this ADR decides. That is the
`third-party/` quarantine's job (ADR 0022 / ADR 0023), not `internal/service`'s. The
SDK ships the evaluation; the vocabulary stays the caller's.

**No relationship model either.** There is no tuple store, no
`object#relation@subject` grammar and no graph to walk. A Zanzibar-shaped model
is a database with a consistency protocol, and half of it is storage this SDK
has no opinion about. RBAC over supplied grants plus ABAC over supplied
attributes answer what a request-path check can answer in nanoseconds.

### D2 — One combining algorithm: deny-overrides. The others are refused by name

`DenyOverrides` is the only combiner in the package. A refusal from any member
wins; an `Allow` is returned only when at least one member permitted and no
member refused; otherwise the composition abstains.

XACML's **permit-overrides**, **first-applicable** and **only-one-applicable**
are refused, and there is no setting that selects them. When one rule permits
and another refuses, one has to lose, and which one is a security property
rather than a preference:

| | failure mode of a wrong rule set | how it is discovered |
|---|---|---|
| deny-overrides | a request that should have been allowed is not | by the user it blocked, in minutes |
| permit-overrides | a request that should have been refused is not | by whoever exploits it |

`first-applicable` is worse than either, because its answer depends on the
order the policies were listed in — so the same rule set has two behaviours
depending on a wiring file's line order, and neither is written down anywhere.

Two consequences that make the choice real rather than nominal:

- **An `Allow` does not short-circuit.** The fold evaluates every member. A
  composition that returned on the first grant would be `first-applicable`
  wearing this function's name. Only a refusal short-circuits, because refusal
  is absorbing: once one member has said `Deny`, nothing any other member can
  say changes the result.
- **Therefore the composition is commutative and associative**, and nesting is
  invisible: `DenyOverrides(a, DenyOverrides(b, c))` and
  `DenyOverrides(a, b, c)` agree, as does any permutation. Pinned by
  `TestDenyBeatsAllowInEveryOrder` and `TestNestingIsInvisible`.

A caller who genuinely needs a grant that beats a refusal — a break-glass path
— writes it as a Go `if` around the composition, where the exception is visible
at the call site instead of hidden in the semantics of a combiner.

The **same** algorithm folds the rule set inside `NewABAC`, so nesting an ABAC
policy inside a `DenyOverrides` composition changes nothing about the answer.

### D3 — Abstention is the third state, and the closure closes toward refusal

`Decision` is `Abstain` / `Allow` / `Deny`. Two properties make it work:

- **The zero value is `Abstain`, and it is first in the `iota` run for exactly
  that reason.** A `Policy` that forgets to set its result says "I have no
  opinion" — not "permitted", which would be a vulnerability produced by a
  forgotten assignment, and not "refused", which would make one forgotten
  assignment veto every other policy it is composed with. This is ADR 0031
  applied to a FUNC port: a func type has no constructor to refuse in, so the
  safe value has to be the zero one. Pinned by
  `TestZeroDecisionIsAbstainAndGrantsNothing`.
- **`Granted()` is the only way to read a verdict, and it is true for `Allow`
  and nothing else.** `d != Deny` is the same expression with one more
  character and a hole in it — it is true for `Abstain`, so it authorizes every
  request no policy recognised, and true for a corrupt value as well.

**What is a request no policy is applicable to?** *It is refused.* Stated
plainly because the ticket that asked for this domain was right that silence is
the worst of the three answers:

- `DenyOverrides()` with no members abstains — the identity of the fold.
- An RBAC evaluator whose grant table confers nothing for this `(action,
  resource)` abstains.
- An ABAC rule set none of whose rules match abstains.
- **`Check` turns every one of those into `PermissionDenied`.**

It is a **refusal**, not an error: an unmatched request is not a fault, and
reporting it as one would make an ordinary "you may not do that"
indistinguishable from a broken deployment in every dashboard. It is also not a
distinguishable refusal — see D4.

`Check` is the *only* place in the SDK where "nobody said `Allow`" becomes a
refusal. Keeping the closed-world default in exactly one place is what lets
every evaluator below it abstain honestly. Note the deliberate contrast with
`internal/service/validation`, where a validator with **no** constraint
**passes**: both are the safe direction for their own domain, which is why ADR
0031 is about *safe* defaults rather than permissive or restrictive ones.

An out-of-contract `Decision` — a numeric conversion, a zeroed field read as a
`Decision` — is treated as `Deny` and reported as `PolicyMisconfigured`. It is
never read as permission.

### D4 — A refusal says that it refused, and nothing else

All four `internal/core/authz` sentinels carry the **byte-identical** `Public`
sentence, `"Access to the requested resource is denied"`, and HTTP 403.

This is the same security property ADR 0046 states for `validation` — a message
names the rule and the bound but never the value — pushed one step further,
because here even the *rule* must not be named. A refusal that explains itself
is a description of the policy set, delivered to the party the policy exists to
keep out:

- *"You are not an admin"* names the role model.
- *"Missing attribute `department`"* names the attribute and tells the attacker
  which value to forge next.
- *"Denied by rule 7"* says how many rules there are and which one to work
  around.

The diagnosis is not lost, it is **moved**. Each sentinel carries a distinct
`Private`, and `Check` attaches structured fields — `outcome` (`deny` /
`abstain` / `unevaluable`), `subject`, `action`, `resource`, and for an
unevaluable outcome the underlying `cause_code` and `cause_reason`. Both reach
the operator through `errs.PrivateOf` / `errs.FieldsOf`, which `pkg/v1`'s own
docs forbid putting on the wire.

The mechanism that makes it hold is `errs`' **origin-wins** rule (ADR 0005 §6),
used deliberately in one direction: `PermissionDenied` is the wrap **origin**
and the cause travels as *fields*. Wrapping the other way round would let an
`AttributeMissing` raised inside a condition become the code a framework routes
on **and** the message the client sees — which is the exact moment a refusal
starts explaining which attribute to forge. Pinned by
`TestTheCauseDoesNotHijackTheCode`.

Four tests hold the property from four sides, and together they are what makes
it a gate rather than a convention:

| Test | What it proves |
|---|---|
| `TestEveryRefusalShowsTheSameSentence` | the four sentinels' `Public` are byte-identical and all 403 — **and their `Private` are all distinct**, so uniformity on the wire did not cost the operator the diagnosis |
| `TestPublicNamesNoAttributeRoleOrRule` | the sentence itself contains none of `role`, `admin`, `attribute`, `rule`, `policy`, `missing`, `subject` (case-insensitive) — the guard against someone "improving" the message by saying why |
| `TestEveryRefusalRendersTheSameSentence` | a `Deny`, an all-`Abstain`, a nil policy and an unevaluable condition produce the **same** `Error()` string, the same `Public` and the same 403 — three causes, one exit |
| `TestTheDiagnosisSurvivesInTheFields` | the same refusal still carries `outcome=unevaluable`, the subject, the action, the resource, `cause_reason=ATTRIBUTE_MISSING` and `cause_code=0.2.26.2` |

The last two are the whole point stated as a pair: **indistinguishable to the
caller, fully distinguishable to the operator.**

`internal/service/authz`'s three sentinels — `GrantInvalid`, `RuleInvalid`,
`ConditionInvalid` — deliberately do *not* follow this rule and carry specific
messages. They are raised while a policy is being **assembled**, on a path
where no request exists and no client is listening, so they may be as specific
as the maintainer needs. A construction error never becomes a response.

### D5 — Resource is the type, not the row; there is no wildcard

Matching is byte equality. `"orders"` and `"urn:acme:orders"` are equally valid
resources; nothing parses, splits or pattern-matches them; a resource literally
named `"*"` is a resource named `"*"`.

The split that makes a wildcard unnecessary: a grant table answers about a
**kind** of thing ("an editor may publish an article") and an instance rule
answers about **one** of them ("…if they wrote it"). Put the type in
`Resource`, put the instance's facts in the attributes (owner, tenant,
classification), then let RBAC answer the first question and an ABAC condition
answer the second.

A wildcard would be a third thing: a matcher grammar, with the precedence and
anchoring bugs that always follow, sitting on the deny path where a
mis-anchored pattern is a hole.

### D6 — The ports are FUNC types (ADR 0039, satisfied structurally)

`Policy` and `Condition` are function types, following `resilience.Operation`,
`scheduler.Job`, `lifecycle.Start` and `validation.Constraint`. ADR 0039's rule
is that a published port must not grow a method, because `pkg/v1` aliases
publish the shape and Go interfaces are structural. A func type satisfies that
rule *structurally* — it cannot grow a method at all. Pinned by
`TestPortsAreFunctionsNotInterfaces`.

### D7 — Absence is not falsity, and unevaluable is absorbing

This is the defect the attribute model exists to prevent, and it has three
parts.

**Attributes are typed, not `map[string]string`.** A string map cannot express
a flag that is present and **false** — `mfa_satisfied=false` and
`mfa_satisfied` absent become the same empty string — and it makes every
numeric comparison a string comparison, in which `"9"` is greater than `"10"`.
Both failures resolve toward permission often enough to be worth four kinds
(`String` / `Int64` / `Bool` / `Strings`) and an explicit accessor. The **kind
is part of the identity**: a text rule against a numeric attribute does not
compare false, it *mismatches*, and the SDK says so.

**A condition that cannot be evaluated returns an error, never `false`.**
`department == "finance"` on a subject with no department is not a comparison
that failed; it is a comparison that never happened. Reporting it as `false`
would let a request carrying **no attributes at all** walk past every "deny
unless X" rule in the system.

**Every combinator treats an unevaluable branch as absorbing**, so the error
cannot be laundered back into a grant:

- `Not` propagates the failure **unchanged** rather than inverting it. This is
  the sharpest case: were absence reported as `false`, `Not(AttrEquals(...))`
  would turn it into `true`, and the subject carrying the *fewest* attributes
  would get the *most favourable* answer.
- `AnyOf` is absorbing too, even when a sibling branch would have held —
  otherwise a request satisfies a rule by omitting the attribute one of its
  branches names.
- `AllOf` and `AnyOf` both evaluate **every** branch, so the answer does not
  depend on the order the caller listed them in.
- `NewABAC` refuses immediately on an unevaluable condition **whatever the
  rule's effect was**: the rule did not run, so nothing it would have decided
  is known, and the only safe reading of an unknown is a refusal.

**An absent attribute is not an empty set.** `AttrStrings("roles")` with no
values states that the subject holds no roles and **abstains** — right for an
anonymous request. *Omitting* the attribute states nothing and is **refused**.
The distinction costs the caller one always-set attribute and closes the case
where a producer bug drops the roles and every "deny unless role X" rule in the
system silently stops firing.

### D8 — No registry, and constructors refuse rather than default

There is no `map[Name]Policy`. A registry's key would have to be a policy name,
and there is no such vocabulary that is not the caller's (D1). Consistent with
`resilience`, `scheduler`, `session` and `validation`.

Construction-time refusals, all raised once at start-up rather than per
request: an empty `RolesAttr` (**no default** — defaulting it to `"roles"` is
the SDK inventing the caller's vocabulary, and a caller who spelled it
`"groups"` gets an evaluator that finds no roles on every request); an empty
grant table; a role conferring nothing; a rule with no `Name`, no `When`, an
empty `Action`/`Resource`, or an `Effect` that is not `Allow` or `Deny` (which
includes the zero `Abstain` — a rule that abstains when it *fires* is a rule
that does nothing); a nil or empty condition set. `Must` / `MustCondition`
panic on these for package-level wiring, in the shape `validation` already
ships.

A **nil member** in a composition **refuses** rather than being skipped.
Skipping it would silently shrink the policy set, which is the single most
valuable edit an attacker could make to a wiring file.

### D9 — RBAC abstains; it almost never denies

`NewRBAC` answers `Allow` or `Abstain`. A request whose `(action, resource)` no
held role confers yields **`Abstain`, not `Deny`** — this is the single most
consequential line in the package, and it is D2 and D3 meeting. An evaluator
that refused where it merely had no grant would be absorbing under
`DenyOverrides` and would veto every request the other policies existed to
permit.

The only two cases where it *does* refuse are not decisions about the request:
the roles attribute is **absent** or carries the **wrong kind**. Both are
checked **before** the grant table is consulted, so a producer bug surfaces on
every request rather than only on the ones a grant would have matched.

`indexGrants` inverts `role → permissions` into `permission → roles` once, at
construction, so evaluation costs one map lookup plus a scan of the roles that
confer *that* permission — one or two in any realistic table — rather than a
walk of the subject's roles against every grant. Growing the table grows the
map, not the check.

## What this domain does NOT guarantee

Stated as loudly as what it does, because every item here is a way to hold a
correct engine wrong.

1. **It does not authenticate.** It takes the subject `session` or `token`
   produced and reads no header, verifies no signature and mints nothing.
   Handing it a subject the caller has not authenticated yields a perfectly
   valid decision about a principal that does not exist.
2. **It does not verify the attributes.** Every `AttrValue` is taken verbatim.
   If a caller attaches attributes derived from client-controlled input, the
   policy authorizes on attacker-supplied facts and the SDK has no way to
   notice. Attributes must be established by the server, from the server's own
   sources, before the check.
3. **It does not enforce.** `Check` returns an error. Nothing here writes a
   response, sets a status or blocks a call. A caller who ignores the return
   value is not authorized — they are unprotected, and no test in this domain
   can see that.
4. **It cannot know the rule set is right.** The engine evaluates faithfully.
   A grant table that confers too much is evaluated exactly as faithfully as
   one that does not.
5. **It says nothing about time-of-check to time-of-use.** A decision describes
   the request as it was at the instant it was evaluated. A role revoked one
   microsecond later does not retroactively refuse work already begun. The
   domain narrows the window by refusing to cache — there is deliberately no
   decision cache, because a cached decision is how a revoked role keeps
   working for five minutes — but it cannot close it.
6. **A `Condition` must not do I/O, and nothing enforces that.** A `Condition`
   receives no `context.Context` precisely so that a blocking lookup has
   nowhere to take a deadline from, but a closure can still capture a database
   handle. A rule that needs a lookup is a `Policy`, which has a context.
7. **There is no audit trail.** The domain produces a decision and, on refusal,
   an error carrying diagnostic fields. It does not record permitted decisions
   anywhere; a consumer who needs an authorization log writes one at the call
   site.

## Consequences / Semantics

- **24th core sibling**; the `internal/core` purpose statement widens. Blocks
  `0.2.26.*` (port) and `0.3.56.*` (evaluators, conditions, construction
  refusals) are allocated in the ADR 0035 ownership table in the same change.
- **`errs.HasCode(err, CodePermissionDenied)` answers true for every refusal**,
  which is what a framework routes on. The three causes are separated by the
  `outcome` field, never by the code.
- **HTTP 403 on every outcome**, including the two that are really evaluation
  faults. A 500 for an unevaluable rule would tell a client to retry a request
  that will be refused identically forever, and would tell an attacker which of
  their inputs the policy could not parse. A framework that would rather answer
  404 to hide the resource's existence overrides it at the edge.
- **`PolicyMisconfigured` additionally carries `EX_CONFIG` (78)**: a
  composition assembled wrong refuses every request forever, and the fix is a
  code change, never a retry.
- **No new dependency, no new kernel primitive**, and nothing in the domain
  waits on the wall clock or starts a goroutine.

## Performance

Measured, not asserted — see `internal/service/authz/BENCH.md` for the full
report and the box that produced it.

- **The evaluation path allocates nothing.** 0 B/op and 0 allocs/op across
  `RBACAllow`, `RBACAbstain`, `ABACAllow` and the full `CheckAllow`
  composition. `AttrValue.Contains` scans the attribute's backing slice in
  place rather than going through `StringsValue`, which clones — the clone
  exists so an attribute stays immutable when it *leaves* the domain, and the
  hot path deliberately never asks for one.
- **~210 ns is the whole check** (RBAC + ABAC + fold + closure). At that cost
  there is no reason to cache an authorization decision — see item 5 above for
  why that matters beyond performance.
- **Refusing costs ~400 B and 2 allocations**, and it should: the refusal is an
  `*errs.Error` carrying six diagnostic fields, which under D4 are the
  operator's *only* view of a denial. The asymmetry is the right way round —
  the permitted path is the one every request takes.
- **`AttrValue` stays 80 bytes**, above the linter's 64-byte by-value
  threshold, for a reason that was measured rather than argued. The obvious
  shrink to 72 B is a trade, not a win: 7–15 % slower on every allocation-free
  evaluation benchmark, ~9 % faster and 128 B smaller on construction, and
  within noise end to end — and it would not silence the rule anyway. The
  evaluation benchmarks are the only ones with a tight enough spread to carry a
  percent-level signal, so they decide. `BENCH.md` records the numbers, the
  spreads, and a first measurement of this experiment that was wrong.

## Why not

- **Why not an interface for `Policy`?** ADR 0039. A published interface can
  grow a method and break every downstream double at compile time; a func type
  cannot grow one at all.
- **Why not two verdicts and a separate "applicable" bool?** That is three
  states with two fields, and it makes the illegal combination
  (`applicable=false, decision=Allow`) representable. An enum makes it
  unwritable.
- **Why not make the combiner configurable?** D2. The setting's only job would
  be to select a less safe algorithm, and the safe one is not the inconvenient
  one — it is the one whose mistakes are visible.
- **Why not a wildcard on `Resource`?** D5. It is a matcher grammar on the deny
  path.
- **Why not return `Deny` from RBAC for an ungranted request?** D9. It makes
  the evaluator uncomposable.
- **Why not an `error` for "no policy was applicable"?** D3. It is an ordinary
  outcome, not a fault, and reporting it as a fault makes a routine refusal
  indistinguishable from a broken deployment.
- **Why not OPA / Cedar / Casbin?** D1. Each is a policy *language* plus an
  evaluator, i.e. exactly the thing this ADR refuses, and adopting one would
  make the SDK a connector to someone else's semantics for every question
  decided above. A consumer who wants one wires it themselves behind the
  `Policy` func type, which is one line and does not commit the SDK.

## Deferred

Named rather than silently absent, so a future reader knows they were
considered:

- **Policy composition other than deny-overrides** — deferred permanently, see
  D2.
- **A decision cache** — deferred deliberately and probably permanently; see
  "does NOT guarantee" item 5.
- **Hierarchical roles** (`admin` implies `editor`) — expressible today by
  listing both roles in the grant table or in the subject's attribute. A
  closure operator would need a cycle check and a defined depth limit; not
  worth it until a consumer asks.
- **An `Attr` kind for time** — deliberately absent. Timestamps travel as
  `Int64` Unix seconds, because an attribute carrying a time *zone* would make
  two requests with the same instant compare unequal.
- **Exponential/negative RBAC ("deny role")** — a role that removes a
  permission is `NewABAC` with a `Deny` effect today, where it is visible as a
  rule instead of hidden as a table entry.
- **An audit hook on permitted decisions** — see "does NOT guarantee" item 7.
  It would have to decide sampling, format and destination, which are three
  more pieces of the caller's vocabulary.

## References

- `internal/core/authz/CLAUDE.md` — the port, the values, the sentinels
- `internal/service/authz/CLAUDE.md` — the evaluators, the combiner, the closure
- `internal/service/authz/BENCH.md` — the measurements this ADR's §Performance quotes
- `pkg/v1/authz/CLAUDE.md` — the public facade
- [ADR 0031](0031-policy-zero-values-are-never-inert.md) — the zero value is a safe default or an explicit refusal
- [ADR 0046](0046-sdk-validation-domain.md) — a message never names the value
- [ADR 0005](0005-sdk-error-codes-dotted-quad.md) §Wrap trail — origin-wins, which D4 depends on
