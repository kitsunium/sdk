# ADR 0046 — Value-validation domain (`validation`)

- **Status**: Accepted
- **Date**: 2026-09-09
- **Deciders**: SDK maintainers
- **Related**: [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal — the trap this domain has two of), [ADR 0028](0028-sdk-config-domain.md) (`config.Validator`, the contract this domain feeds), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port is extended by a sibling, never by widening — satisfied here structurally), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (published concrete shapes), [ADR 0041](0041-sdk-scheduler-domain.md) (the FUNC-port precedent), [ADR 0016](0016-sdk-process-supervision-domain.md) / [ADR 0026](0026-sdk-resilience-domain.md) / [ADR 0029](0029-sdk-net-domain.md) / [ADR 0042](0042-sdk-token-domain.md) (the no-registry core-sibling precedents), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (codes and range ownership), [ADR 0019](0019-pkg-errs-public-construction.md) (the third-party Major range a consumer-written constraint uses)
- **Amends**: `internal/core`'s purpose statement — `validation` is its 15th sibling

## Context

Every service that accepts input from outside itself answers the same question
— "is this acceptable?" — and every one of them answers it with a hand-written
cascade of `if` statements that returns the first thing it finds. Three defects
follow, and they are always the same three.

**It does not say where.** `errors.New("invalid address")` on a request body
with an array of addresses tells the client to guess. On a nested structure the
answer has to be a *path*, and a path has to have a grammar, or every service
invents its own and the client parses none of them.

**It stops at the first failure.** A registration form that reports "name is
required", then "password too short", then "country not allowed" makes the user
submit it three times. Collecting is strictly more work than short-circuiting,
so the shortcut wins by default unless the engine takes the other side.

**Its rules cannot be reused.** The cascade is welded to the struct it checks,
so the "port must be 1..65535" written in the HTTP handler is written again in
the config loader, and they drift.

The SDK already has one validation-shaped thing, and it is not this. ADR 0028's
`internal/core/config.Validator` is `Validate() error`: a decoded config
struct's SELF-check, asked once by `config.Load`, answering yes or no. It has
no notion of a violation, of a location, or of composition, and it is scoped to
config. It is a contract in want of an engine, and this ADR is the engine —
which is why the first thing recorded below is how the two articulate.

## Decision

1. **Add `internal/core/validation`** — a core sibling declaring three things
   and nothing else: the `Constraint[T]` port, the `ViolationValue` it reports,
   and the `ReportValue` that collects them, plus the path grammar (`RootPath`
   / `JoinField` / `JoinIndex`) and two typed sentinels. **No registry** — one
   engine and one rule vocabulary, so a registry would be over-abstraction (the
   `proc` / `resilience` / `net` / `scheduler` / `token` precedents).

2. **The port is a FUNCTION type.**

   ```go
   type Constraint[T any] func(path string, value T) ReportValue
   ```

   ADR 0039's rule is that a published port must not grow a method, because a
   `pkg/v1` alias publishes the *shape* and Go interfaces are structural. A func
   type satisfies it **structurally**: it cannot grow one at all. This is
   ADR 0041's precedent (`scheduler.Job` / `scheduler.Schedule`) applied for the
   same reason, and `TestPortIsAFunctionNotAnInterface` is the executable guard.

   A constraint that ACCEPTS returns a nil report, so the accepting path — the
   one taken on every field of every valid request — builds nothing.

3. **`internal/service/validation`** ships the constraints, the combinators
   (`All` / `First` / `Field` / `Each` / `Check` / `Must`) and the struct-tag
   front end (`Struct`), with a plan cache keyed on `(type, mode)`.

4. **`pkg/v1/validation`** aliases the port and the two values, and re-exports
   everything else as thin delegations.

5. **Two error blocks, split along the layer boundary.** Core owns `0.2.15.*`
   for what the *port* can go wrong about — `VALIDATION_FAILED` (the aggregate
   an `error`-typed contract sees, HTTP 422) and `CONSTRAINT_MISCONFIGURED`
   (`EX_CONFIG`, emitted by service constructors the way `service/resilience`
   emits `core/resilience`'s `PolicyMisconfigured`). Service owns `0.3.47.*` for
   the *rule identities* — `REQUIRED`, `OUT_OF_RANGE`, `LENGTH_OUT_OF_RANGE`,
   `NOT_IN_SET`, `PATTERN_MISMATCH` — plus the tag compiler's `INVALID_RULE` and
   `UNSUPPORTED_TARGET`. Which rules exist is a service decision; that a
   validation can fail is a port fact.

### How this articulates with `config.Validator` — it feeds it, it does not replace it

`config.Validator` keeps its shape and its meaning. It is the *contract*: a
decoded struct declares that it can check itself, and `config.Load` calls it
after decoding, aborting on a non-nil return with `CONFIG_VALIDATION_FAILED`.

This domain is the *engine* a struct uses to honour that contract:

```go
func (c Conf) Validate() error {
    rules, err := validation.Struct[Conf](validation.StructConfig{})
    if err != nil {
        return err
    }
    return validation.Check(c, rules)
}
```

Three consequences, all deliberate:

- **`internal/service/validation` does not import `internal/core/config`, and
  `config` does not import `validation`.** The bridge runs in the CALLER's
  method. Making either a dependency of the other would couple two domains that
  only need to compose, and would force every `validation` consumer to link the
  config loader.
- **The caller sees `CONFIG_VALIDATION_FAILED`, not `VALIDATION_FAILED`** —
  `config.Load` wraps, because `Load` is the contract they called. The
  validation error is the cause and stays reachable through the chain.
- **Detail is lost at that boundary, and that is stated rather than papered
  over.** `Validate() error` can carry one error; the full report is a
  `ReportValue`. A caller who needs every path validates through the engine and
  reads the report. What survives into the error is the count, the first rule
  and the *list of paths* — enough for a log line an operator can act on.

### Decision 1 — a violation says WHERE

**Path grammar**: members joined by `.`, elements suffixed `[n]`, no separator
before a bracket. `RootPath` is the empty string and means the value as a whole.

```
""                        the value itself — a cross-field rule
user                      a member
user.address.zip          nested members
user.addresses[2].zip     element 2 of a member, then its member
[0]                       element 0 of a value validated at the root
```

Paths are built with `JoinField` / `JoinIndex` and never by concatenation, so
the grammar has exactly one implementation.

**The member name is the `json` tag's when the field has one, else the Go field
name.** This is not a preference between "wire names" and "Go names"; it is a
fact about this SDK. `internal/service/config.Load` decodes *every* format —
TOML, YAML, env, JSON — through a `json.Marshal`/`json.Unmarshal` round trip
(ADR 0028), so the json tag is literally the key the operator typed in their
file. A path built from Go field names would name something that appears
nowhere in the input the user is being asked to fix. The programmatic `Field`
takes the name as an argument, so the caller is always in control.

The same fact decides embeddings. An embedded struct with no json name — or a
pointer to one — adds **no** segment: `encoding/json` promotes its fields into
the enclosing object, so `Common.zip` names nothing the operator can write and
the path is `zip`. A json-named embedding keeps its name, and an embedded
non-struct is keyed by its type name, exactly as `encoding/json` keys it.
(Amended 2026-09-11: the first version added the Go name of every embedding.)

**A map key is NOT in the grammar**, and that is a decision, not an omission —
see *Deferred*.

### Decision 2 — every violation, and violations are not errors

**Collecting is the default.** `All` runs every constraint; `Each` visits every
element; the tag plan walks every field. Stopping is the opt-in, and it is a
**combinator** (`First`) rather than a mode flag, plus `StructConfig.StopAtFirst`
which is compiled INTO the plan and into its nested plans. Both really stop —
the later constraints are never evaluated. A flag that truncated a full report
afterwards would claim a saving it did not make, and `TestFirstReallyStops`
counts the evaluations to prove the difference.

**A `ViolationValue` carries an `errs.Code` but is not an `error`.** The code is
there so a caller routes with `errs.HasCode` / `errs.NewPrefixMatcher` instead
of string-matching a rule name — the same instrument every other SDK domain
offers. It is not an error because a validation normally produces *several*, and
`errors.Join` of five renders five bracket headers, loses the ordering and
buries the paths, while a single error would have to pick one violation to be
about.

**The aggregate is not an `error` either — it has an `Err()` method.**
`ReportValue` is `[]ViolationValue`, so its zero value (nil) is a *passing*
report and composition is `append`. It deliberately does not implement `error`:
a value type that implements `error` and is returned by value makes
`if err != nil` true for a passing validation — the typed-nil trap that
`errs.Wrap` has an explicit case for (ADR 0005 §3.6, case 2). `Err()` returns
the interface, so nil means nil, and `TestEmptyReportIsPassingAndItsErrIsNil`
pins it.

**A message never contains the value.** This is a security property, not a
style rule, and it has its own test (`TestNoMessageEchoesTheValue`). A
validation message is the one error message in a service *designed* to reach the
end user; a message that echoed the value would exfiltrate whatever was
validated — a password, a token, a card number. Every built-in message names the
rule and the bound, both of which are the caller's own literals. `Err()`'s
fields carry paths and counts for the same reason. The precedent is already in
the tree: `service/id.Malformed` carries "a `rule` field naming which check
fired, never the input itself".

### Decision 3 — both front ends, and the tag path is measured

The programmatic path is **generic and reflection-free**: `Field` and `Each`
take an accessor function, so every descent is a direct field read the compiler
can inline, and every rule is bound to a concrete type at compile time. There is
no `reflect` import on that path and no type switch.

The tag path buys ergonomics with `reflect`, and the SDK undertook to measure
the trade rather than assert it. `Struct[T]` compiles a **plan** — resolved
field indices, kind-bound closures, nested plans — once per `(type, mode)`, into
a `sync.Map`. Measured on a `User` with three scalar rules and a dived slice of
three `Address` (see `internal/service/validation/BENCH.md`):

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| programmatic, value accepted | 705 | 96 | 6 |
| **tag, value accepted** | **890** | **144** | **7** |
| `Struct[T]` on a compiled type | 103 | 24 | 1 |
| one full plan compilation | 5 003 | 1 424 | 39 |

**The cache is worth 48×** (5.0 µs to compile, 103 ns to fetch): without it a
validation of this shape would cost ~5.9 µs instead of ~1.0 µs, with string
splitting and `reflect.StructField` lookups on the hot path of every field of
every request. **Reflection itself costs 26 %**, not an order of magnitude —
185 ns and one allocation — because the plan leaves nothing to discover at
validation time. And `Struct[T]` at 103 ns is what makes the `config.Validator`
bridge above honest: the method can look the plan up on every call.

Both front ends produce the **identical violation** — same path, rule name and
code — which `TestTagAndCodePathsAgree` pins, so moving a rule from a tag into
code cannot silently change what a client sees. And because `Struct[T]` returns
a `Constraint`, tags and hand-written rules compose into one report:
`All(tagRules, crossFieldRule)`.

### Decision 4 — the constraint set is a decision, not a list

Every constraint shipped is maintained forever, so the set is small and each
entry answers a question the SDK can answer **completely**.

**Shipped** (programmatic; the tag spelling in brackets):

| Constraint | Question | Tag | Code |
|---|---|---|---|
| `Required[T comparable]()` | is it there? | `required` | `0.3.47.1` |
| `AtLeast` / `AtMost` / `Between` (`cmp.Ordered`) | is it in range? | `min=` / `max=` | `0.3.47.2` |
| `Length(min,max)` — string, **runes** | is it the right size? | `minlen=` / `maxlen=` | `0.3.47.3` |
| `Count[E](min,max)` — slice elements | how many? | `mincount=` / `maxcount=` | `0.3.47.3` |
| `OneOf[T comparable](...)` | is it in the set? | `oneof=a\|b\|c` | `0.3.47.4` |
| `Matches(pattern)` — RE2 | does it match? | *refused in tags* | `0.3.47.5` |

Two stated limits rather than hidden ones. `Required` cannot distinguish "not
supplied" from "supplied as zero" — no constraint over a Go value type can, and
the fix is a pointer field, which the doc says. `Length` counts runes, not
grapheme clusters: clustering needs a Unicode table the SDK does not carry, and
claiming otherwise would be worse than saying so.

**Refused, and refused BY NAME** — the message says what to do instead, because
"unknown rule" leaves the caller unsure whether they mistyped or asked for
something that does not exist:

| Refused | Why, and what instead |
|---|---|
| `email` | RFC 5322 addresses are **not a regular language**: quoted local parts, comments, domain literals and IDN all sit outside what any pattern can decide. Every `email` rule in every library is a guess that rejects valid addresses and accepts unroutable ones — and deliverability is decided by *sending mail*, not by parsing. The SDK will not put its name on that. → check it is present, send a confirmation, or write your own `Matches`. |
| `url` | A URL that parses is not a URL that is acceptable: scheme allow-lists, host resolution and SSRF are application decisions. → `net/url.Parse` plus your own policy. |
| `uuid` | Six versions, six shapes, and a shape check on an opaque identifier belongs to whoever defines it. → `pkg/v1/id`. |
| `pattern` / `regex` **in a struct tag** | The tag is comma-separated and a regexp contains commas far more often than not. Accepting it would mean inventing an escape dialect, and a **silently truncated pattern** is precisely the class of failure that makes a validator worse than no validator. → compose `Matches` in code; it composes with the tag validator into one report. |
| `dive` into a map | See *Deferred*. |
| **business rules** | Structurally: they have no vocabulary here. A rule that knows what an order, a tenant or a discount is belongs to the application, and it writes one as a `Constraint` and hands it to `All`. |
| any unknown rule | Named, with the accepted dialect listed. |

### The ADR 0031 trap, which has two sides here

They are opposite and must not be confused:

- **A validator with no constraint is legitimate and PASSES.** `All()` accepts.
  A type with no `validate` tag compiles to an empty plan and accepts. Refusing
  it would make it impossible to adopt validation one field at a time, and
  "nothing is configured" is a coherent state — the same reasoning ADR 0041 used
  for a scheduler with no entries.
- **A constraint that cannot be honoured is REFUSED AT CONSTRUCTION.** An
  inverted interval (`Between(10, 1)`), a negative size, an empty `OneOf` set,
  an uncompilable pattern, a nil accessor, an unnamed member, a tag rule the
  field's kind cannot answer, a self-referential type. Every one of them would
  otherwise produce a validator that quietly rejects — or quietly accepts —
  everything it is shown, while presenting a plausible per-field message.

The failure is not "nothing is validated". It is "something is validated by a
rule that cannot work, and nobody was told".

A constructor that has **no** way to be misconfigured — `Required`, `AtLeast`,
`AtMost`, `All`, `First` — returns a `Constraint` directly rather than a
`(Constraint, error)`, so the two-valued signature is a signal rather than
ceremony.

### Cross-platform (ADR 0018)

100 % portable Go (`reflect`, `regexp`, `strconv`, `strings`, `sync`, `cmp`,
`unicode/utf8`). No OS-specific code, no clock, no I/O; the build bar and the
runtime bar are both trivial on all 8 GOOS.

## Consequences / Semantics

- **15th core sibling, no registry.** `pkg/v1` gains a dep-light facade (stdlib
  + kernel + core + service only). Docs and `docs/error-codes.yaml` updated in
  the same change, per rule 11.
- **`required` and its neighbours are PEERS, not a gate.** An absent string
  trips `required` **and** `minlen`, and both are reported. That is collect-all
  doing its job: the report describes what a valid value looks like, not only
  the first thing wrong with this one. A caller who wants one message per field
  uses `StopAtFirst`, or filters on `Rule`.
- **`dive` is explicit.** A nested struct is walked only when its field says so.
  Automatic recursion would walk into `time.Time`, `net.IP` and every other
  struct that happens to be a field, and a rule engine that silently reaches
  places the author did not name is one whose report cannot be trusted. A type
  that reaches **itself** is refused at compile time; two fields of the same
  type (a diamond) compile fine.
- **A nil pointer contributes nothing.** `dive` through a nil pointer descends
  into nothing — a nil `*[]T` or `*[N]T` included — and requiring it to be
  present is `required`, a different question asked at the field. For a slice
  of pointers, `dive,required` asks that question of each ELEMENT pointer: a
  nil element is the violation, exactly as `required` on a `*T` field and
  `Each(…, Required[*T]())` mean it. (Amended 2026-09-11: `dive` on a pointer
  to a collection used to panic, and `dive,required` asked about the pointee,
  so a nil element passed.)
- **`Matches` runs RE2.** Go's regexp does not backtrack, so a pattern cannot be
  turned into a denial of service by the value it is shown. That is what makes
  it acceptable to run one on untrusted input, and it is why the SDK imposes no
  input-length cap of its own here.
- **`ReportValue` and `ViolationValue` are published concrete shapes**
  (`pkg/v1/validation.Report` / `.Violation` alias them), so ADR 0040 applies:
  they may still change while the module is v0, said out loud, and not after v1.
- **Report order is stable**: composition order, then declaration order, then
  index order. Two runs produce the same report, which is what makes two reports
  diffable — and is why the map descent is deferred rather than shipped with an
  arbitrary iteration order.
- **A compile FAILURE is not cached.** It is a source defect that fails the same
  way every time, and caching it would only make the second error harder to
  trace.
- **The Go constructors are `AtLeast` / `AtMost`; the tag spelling stays
  `min=` / `max=`; the reported `Rule` is `min` / `max`.** Exported `Min` and
  `Max` shadow Go's own builtins at every call site, which `ktn-linter`'s
  `KTN-FUNC-MINMAX` refuses and which reads badly next to `min(a, b)`. The
  reported rule name follows the TAG rather than the function, because the tag
  is the spelling a user reads in the struct they are fixing — and because both
  front ends must report the same rule for `TestTagAndCodePathsAgree` to mean
  anything.
- **`Must` panics, on purpose and only there.** It is the
  `regexp.MustCompile` / `template.Must` idiom for a package-level validator
  built from source literals at init: the binary must not start. Its doc says
  plainly not to use it on a bound that comes from configuration.

## Breaking changes

None. `validation` is a new domain in this change set — there is no prior
published surface to break. `internal/core/config.Validator` is untouched.

## Alternatives considered

- **Extend `config.Validator` into the engine.** Rejected on ADR 0039 and on
  scope: `Validate() error` is aliased by `pkg/v1/config.Validator`, so adding a
  method breaks every downstream implementer at compile time with no deprecation
  window — and the result would still be config-only, while the same rules are
  needed on an HTTP body and a CLI flag set.
- **Make `ReportValue` implement `error`.** Tempting, and it is the single most
  common way this API is written elsewhere. Rejected because a value type
  implementing `error` and returned by value makes `if err != nil` true for a
  passing validation; the alternative is returning a typed nil, which is the
  same bug wearing a different hat.
- **Make `Violation` an error and return `errors.Join`.** Rejected: five joined
  errors render five bracket headers, the paths are buried in prose, and the
  ordering — the thing that makes a report diffable — is not part of the
  contract.
- **A relative path with prefix rewriting.** Constraints could report at a path
  relative to their own subtree and each combinator could rewrite the prefix
  afterwards; that moves ALL path construction onto the failure path and takes
  the accepting path to zero allocations (from 6 / 96 B). Rejected: it makes the
  `path` argument mean something different depending on where a constraint was
  composed, and obliges every hand-written constraint to return a freshly
  allocated report the caller may mutate. 96 bytes next to the JSON decode that
  produced the value does not buy a two-line port becoming a puzzle.
- **Stop-at-first as a flag on a runner.** Rejected as dishonest: a flag applied
  to a finished report truncates rather than saves. `First` is a combinator and
  `StopAtFirst` is compiled into the plan, so both really stop.
- **Automatic descent into nested structs.** Rejected: it walks into
  `time.Time`, and a rule engine that reaches places the author never named
  produces a report nobody can audit. `dive` is one word.
- **Wrap a third-party validator.** Rejected for the reason ADR 0041 gives about
  cron libraries: the popular ones bring their own dialect decisions — `email`,
  `url`, automatic recursion, a first-failure default — which is exactly the set
  of choices this ADR exists to make deliberately, and they bring the transitive
  dependencies `pkg` consumers are kept away from.
- **Ship `email` with a documented subset.** Considered seriously, because it is
  the single most requested rule. Rejected: any subset the SDK could describe
  ("has an `@` with something either side") is one the caller can write in a
  line, and shipping it under the name `email` would let a reader believe the
  SDK had answered a question it had not. Refusing it *by name*, with the reason
  and the alternative in the message, is more useful than a rule that is
  approximately right.

## Deferred

- **Map descent.** `dive` into a map is refused by name. Two things are missing,
  not one: a total order over the keys (without it the report order is Go's
  randomised map iteration, and "collect everything" stops being diffable), and
  an unambiguous rendering of an arbitrary key in the path grammar. Both are
  answerable — sorted `cmp.Ordered` keys, `%q`-quoted segments — and neither
  should be answered in the same change that establishes the grammar.
- **A `Violation` → message catalogue / i18n.** `Rule` + `Path` + `Code` are
  deliberately enough for an application to look up its own translated string.
  The SDK will not ship a message bundle.
- **Conditional rules** (`required_if`, `required_with`). They need a
  cross-field vocabulary in the tag dialect, which is where every tag dialect
  turns into a language. A cross-field `Constraint` composed with `All` covers
  the same ground today, in Go, with the compiler checking it.
- **A `Validate` that mutates** (trim, normalise, default). Validation that
  writes is a different operation with a different contract, and conflating them
  is how "validate" ends up meaning three things in one codebase.
- **`errs.Code` on a `ViolationValue` for consumer-defined constraints beyond
  the third-party Major range.** ADR 0019's `0x40–0x7F` reservation already
  covers it; nothing more is needed until someone reports otherwise.

## References

- Impl: `internal/core/validation/`, `internal/service/validation/`,
  `pkg/v1/validation/`.
- `internal/service/validation/BENCH.md` — the measured cost of both front ends
  and of the plan cache.
- `internal/core/config/validator.go` — the contract this domain feeds.
- ADR 0031 (clamp vs refuse), ADR 0039 (published ports), ADR 0041 (the FUNC
  port precedent), ADR 0028 (config's JSON round-trip decode, which is why the
  path uses the json name).
