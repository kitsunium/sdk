# internal/service/validation/

## Purpose

The concrete constraint engine over `internal/core/validation`: a **closed** set
of built-in constraints, the combinators that compose them and descend into
nested structures, and a **struct-tag front end** that compiles a cached plan
per type. ADR 0046.

Code range: `0.3.47.*` (the built-in rule identities + the tag compiler's
refusals). It also emits core sentinels `0.2.15.*` for every constructor
refusal.

## Contents

| File | Surface |
|---|---|
| `validation.go` | `All` / `First` / `Check` — composition and the error bridge |
| `descend.go` | `Field` / `Each` / `Must` — reflection-free descent |
| `presence.go` | `Required[T comparable]` |
| `bounds.go` | `AtLeast` / `AtMost` / `Between` over `cmp.Ordered` |
| `size.go` | `Length` (string runes) / `Count` (slice elements) / `Unbounded` |
| `set.go` | `OneOf[T comparable]` |
| `pattern.go` | `Matches` (RE2, compiled at construction) |
| `struct.go` | `Struct[T]` / `StructConfig` — the tag front end |
| `plan.go` | the compiled `structPlan` and its `(type, mode)` cache |
| `plan_key.go` | `planKey` — the `(type, mode)` identity of a compiled plan |
| `compile.go` | the tag compiler: field walk, name and path-segment derivation (`promotedEmbedding`), tag parsing (`splitDive`, `splitPresence`) |
| `dive.go` | the `dive` descent — nested structs, slices, arrays, and one pointer to any of them |
| `element_plan.go` | `elementPlan` — one dived element's rules, presence asked of a pointer element before it is followed |
| `rules.go` | the dialect: accepted rules, and the ones refused BY NAME |
| `rules_kind.go` | kind-bound bound/size checks resolved at compile time |
| `rules_oneof.go` | kind-bound set membership |
| `reject.go` | the refusal helpers (`rejectConstraint` / `rejectRule` / `rejectTarget`) |
| `violate.go` | `one` — the single place a `ViolationValue` is minted |
| `rule_names.go` | the closed set of reported rule names |
| `BENCH.md` | the measured cost of the two front ends and of the plan cache |

## The constraint set is a decision, not a list

Everything shipped here the SDK maintains forever, so the set is deliberately
small and each entry answers a question the SDK can answer **completely**:

**Shipped** — presence (`Required`), ordered bounds (`AtLeast`/`AtMost`/`Between`),
size (`Length` in runes, `Count` in elements), set membership (`OneOf`),
pattern (`Matches`, RE2).

**Refused, by name, with the fix in the message** — `email`, `url`, `uuid`
(each names what to do instead), `pattern` in a *tag* (a regexp cannot live in
a comma-separated tag without inventing an escape dialect, and a silently
truncated pattern is the failure this package exists to prevent), `dive` into a
map, and any unknown rule. Business rules are refused structurally: they have
no vocabulary here, and a caller writes them as a `Constraint` and composes.

## Conventions

- **Collect-all is the default, and `First` really stops.** `First` is a
  combinator rather than a mode flag, so it cannot claim a saving it did not
  make. `StructConfig.StopAtFirst` is compiled INTO the plan — nested plans
  included — so a stop-at-first tag validator never reads the later fields.
- **`required` and its neighbours are peers, not a gate.** An absent string
  trips `required` AND `minlen`, and both are reported. Collect-all means the
  report describes what a valid value looks like, not only the first thing that
  is wrong with this one.
- **Every fallible constructor refuses at CONSTRUCTION** with the core
  `ConstraintMisconfigured` sentinel: an inverted interval, a negative size, an
  empty `OneOf` set, an uncompilable pattern, a nil accessor, an unnamed member.
  A constraint nothing can satisfy is not a strict rule, it is a broken one
  (ADR 0031). A constructor with no way to be misconfigured — `Required`,
  `AtLeast`, `AtMost`, `All`, `First` — returns a `Constraint` directly.
- **A nil constraint in `All`/`First` is skipped; a nil accessor in
  `Field`/`Each` is refused.** They are different nils: an absent rule is a
  legitimate rule set, while an absent accessor means the rules exist and can
  never run — an inert validator wearing a working one's face.
- **A message names the rule and the bound, NEVER the value.**
  `TestNoMessageEchoesTheValue` runs every built-in against a probe value and
  fails if any repeats it. A validation message is the one error message in a
  service designed to reach the end user.
- **Every message is built at CONSTRUCTION**, not per validation — which is
  also why the bounds are echoed: they are the caller's own literals.
- **The plan is compiled once per `(type, mode)`** and cached in a `sync.Map`.
  A compile FAILURE is not cached: it is a source defect that fails the same
  way every time. See `BENCH.md` — compiling is 5.0 µs, fetching is 103 ns.
- **`dive` is explicit.** A nested struct is never walked automatically:
  automatic recursion would walk into `time.Time`, `net.IP` and every other
  struct that happens to be a field. A type that reaches itself is refused at
  compile time; two fields of the same type (a diamond) compile fine.
- **`dive` follows exactly one pointer — to a struct or to a collection.**
  `*Address`, `*[]Item` and `*[N]Item` all dive, and a nil one contributes
  nothing: requiring it is `required`, asked at the field. The collection step
  used to drop the pointer the compiler had resolved, so a `*[]T` field
  compiled and then panicked in `Len` on every validation.
- **After `dive`, `required` on a POINTER element asks about the pointer.** A
  nil element is a violation at `items[i]`; a non-nil pointer to a zero value
  is present — what `required` means on a `*T` field, and what
  `Each(…, Required[*T]())` means in code. Every other element rule, and the
  element's nested plan, asks about the value behind it, so a nil element is
  vacuous for them and plain `dive` still passes over one in silence. The
  presence rule used to run only after a nil element had already returned.
- **A path names what JSON keys, embeddings included.** A member is its json
  tag's name, else its Go name — and an embedding JSON PROMOTES (anonymous, a
  struct or `*struct`, no json name) adds no segment at all, because its members
  are keys of the enclosing object: `Common.zip` names nothing an operator can
  write. `promotedEmbedding` is `encoding/json`'s own rule, so a json-named
  embedding keeps its segment, `json:"-"` keeps the Go name, and an embedded
  non-struct is keyed by its type name. A refusal still names the field by its
  `pathName`, since a refusal is read by the developer looking for it.
- **A numeric literal is read at the FIELD's width** (`reflect.Type.Bits`), in
  `min`/`max` and in every `oneof` entry. A literal the width cannot hold is
  refused as `INVALID_RULE`, naming the kind: `min=200` on an `int8` could never
  be met and `max=300` on a `uint8` could never fail (ADR 0031). A literal that
  fits is rounded as Go rounds a constant of that type, which is what keeps
  `max=0.1` on a `float32` inclusive of the float32 its literal spells — and
  also why a float literal below the type's smallest magnitude reads as zero,
  exactly as the same constant would in Go.
- **The tag path and the programmatic path produce the SAME violation** — same
  path, rule name and code. `TestTagAndCodePathsAgree` pins it, so moving a
  rule from a tag into code cannot silently change what a client sees.

## Do NOT

- **Add a constraint without deciding it.** Each one is maintained forever, and
  a half-right rule (an "email" regexp) is worse than none: it rejects valid
  input and accepts unroutable input while looking authoritative.
- **Widen the tag dialect to carry a regexp.** The comma is taken.
- **Parse a tag outside `compile.go`.** Everything in the compiler runs once per
  type; anything that runs per validation belongs in a closure the compiler
  built.
- **Echo a validated value into a message, a field, or an error.**
- **Import `internal/core/config`.** The bridge runs in the CALLER's `Validate`
  method; making it a dependency here would invert the layering and couple two
  domains that only need to compose.

## Verification

```
bazel test --config=race //internal/service/validation:validation_test
# OR
cd internal/service && GOWORK=off go test -race ./validation

# benchmarks (regenerates the numbers in BENCH.md)
cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./validation/
```
