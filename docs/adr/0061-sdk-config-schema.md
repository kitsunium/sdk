# ADR 0061 — Configuration schema: required keys, typed defaults, and a closed vocabulary

- **Status**: Accepted
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0028](0028-sdk-config-domain.md) (the `config` domain this extends — no new domain, no new `PP` range), [ADR 0046](0046-sdk-validation-domain.md) (the constraint engine this FEEDS from, and does not duplicate), [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a zero value is a safe default or an explicit refusal — used three times here), [ADR 0039](0039-extending-a-published-port-without-breaking-it.md) (a published port is not widened — `Validator` is neither widened nor bypassed), [ADR 0040](0040-changing-a-published-shape-while-v0.md) (the v0 licence used, and named), [ADR 0005](0005-sdk-error-codes-dotted-quad.md) / [ADR 0035](0035-pp-range-ownership-enforcement.md) (codes and range ownership)
- **Amends**: nothing. `config` is ADR 0028's 10th core sibling and stays exactly where it was; this ADR adds a second entry point beside `Load`, and three serials to a block `core/config` already owns

## Context

ADR 0028 gave the SDK a configuration loader: layer some `Source`s, merge them
later-wins, decode through a JSON round trip, and call `Validate()` if the
decoded struct implements it. That is a complete answer to "how do I read
configuration" and no answer at all to "what configuration does this
application expect".

The gap is not cosmetic. Three failures follow from it, and all three are
production incidents rather than style complaints.

**A missing key is discovered by the code that reads it.** A service whose
`database.dsn` is unset starts, binds its port, reports itself healthy, and
fails on the first request — or worse, on the first request that happens to
touch that path, which may be an hour later and on one replica. The decoded
struct holds `""`, which is indistinguishable from a deliberate empty string,
so nothing on the way up had anything to complain about.

**A typo is silent.** `APP_PORTT=9090` produces a merged key `portt`, which
`encoding/json` drops because no field addresses it, and the process starts on
whatever `port` was before. The evidence is the *absence* of an effect: nothing
logs, nothing fails, and the operator concludes the variable does not work and
tries a different name.

**A default is applied after the decode, or not at all.** The idiom is `if
c.Timeout == 0 { c.Timeout = 30 }`, which cannot tell "the operator did not
configure a timeout" from "the operator configured a timeout of zero" — and
therefore overrules the operator on every field whose zero value is meaningful,
which is most of them.

What the SDK already has, and what it must not grow a second copy of: ADR 0046's
`validation` domain answers "is this VALUE acceptable" with a located,
collect-all report, and ADR 0046 states explicitly that it *feeds*
`config.Validator`. A schema that grew its own `min`/`max`/`oneof` vocabulary
would be that second copy, and the two would drift the first time one of them
gained a rule.

## Decision

`config` gains a **schema**: a compiled declaration of which keys an application
reads, which of them it cannot start without, what each holds when nobody
supplied it, and one place to hang the cross-field rule a per-field tag cannot
express. It arrives as a **second entry point** — `NewSchema` + `LoadSchema`
beside the existing `Load` — not as a widened signature, because a load with no
schema is a legitimate load and there must be a way to spell it.

The schema describes the **SHAPE**. It says *which keys*, *required or not*, and
*what they default to*. It says nothing about what a value may contain: that is
`validation`'s job, and the schema composes that engine rather than
reimplementing it.

### Decision 1 — a required key fails the LOAD, and every missing key is named at once

`SchemaSpec.Required` lists keys. A key listed there and absent from every layer
fails `LoadSchema` with `CONFIG_KEY_MISSING` — at start-up, before the process
has a configuration to run on. That is the entire reason a schema exists; a
requirement discovered at first access is a requirement that was not declared.

The check runs on the **merged map, before the decode**, and that position is
the whole argument. After the decode an absent key and a key set to its zero are
the same bytes, so no constraint over the decoded value can distinguish them.
Before it, the question is trivial: the key is either an entry in the map or it
is not.

Every missing key is reported in **one** error, with the count and the whole
list. Reporting the first would make an operator restart the service once per
missing key to discover the next, which is exactly the failure ADR 0046 states
its collect-all rule to prevent. The two domains agree, and they agree for the
same reason.

**A key requirement and a value requirement are different statements, and both
exist.** `SchemaSpec.Required` is the key-level one. `validate:"required"` is the
value-level one, which ADR 0046 documents as refusing the zero value and being
unable to tell an absent field from an explicitly-zero one. They disagree
usefully: `port = 0` written in a file **satisfies** the schema (the key was
supplied) and **fails** the tag (the value is zero). Declare both when both are
meant; the SDK will not merge them into one word that means neither.

An explicit `null` counts as **supplied**, for the same separation: the operator
wrote the key, and whether the resulting zero is acceptable is the validation
domain's question.

**The key pass is not merged into the value report.** A key nobody supplied
decodes to a zero the operator never wrote; running the bounds on that zero
would report a `min=1` violation nobody committed and bury the one actionable
fact under a fact derived from it. This is not a truncated report — every
missing key and every unknown key is reported, all of them, in that one pass.
It is the second pass that is not run, over input already known to be fiction.

### Decision 2 — an unknown key is REFUSED, and the escape hatch is named

A key that no field of the target type addresses fails the load with
`CONFIG_UNKNOWN_KEY`. The zero value of `SchemaSpec` refuses; opting out is
`AllowUnknownKeys: true`.

**Why refuse.** A schema is the statement "these are the keys I read". A key
outside it is either a mistake or a deliberate extra, and only one of those two
is common. The mistake — `APP_PORTT`, `databse.host`, `max_conn` for
`max_conns` — is the incident described above, whose whole character is that it
produces no signal at all. ADR 0031's rule decides which way the zero value
points: the zero value is the choice made by someone who has not yet learned the
question exists, so it must not be the dangerous one. `AllowUnknownKeys: false`
is what a schema means; a schema that ignored unknown keys by default would be a
declaration of intent that declines to act on itself.

**Why the escape hatch is real and not grudging.** Two legitimate shapes exist:
a configuration file shared by several services, where each reads its own
subtree, and `EnvSource("")`, which hands over the entire process environment —
`PATH`, `HOME`, and everything else the shell exported. Both are facts about the
SOURCE, not about the schema, and the opt-out is where the author says so. This
is stated in `EnvSource`'s own doc comment, because the operator who hits it
will be reading that and not this.

**Where the walk stops.** The check walks the merged document against the type's
vocabulary, and every key is classified `leaf` or `table` when the schema is
compiled. Below a `map[string]any`, a slice, or a type that decodes itself
(`time.Time`, `net.IP` — decided by a property, `json.Unmarshaler` /
`encoding.TextUnmarshaler`, never by a list of names), there are no keys: there
is one value the decoder owns. Descending there would report a legitimate
document's contents as a typo. An unknown key is reported and **not** descended
into either, because everything below it is unknown for the same reason and the
parent is what an operator can act on.

The report is **sorted**. Map iteration is random, so an unsorted list would
differ between two runs of the same deployment: undiffable in a log, unpinnable
in a test.

### Decision 3 — a default is a LAYER, and a defaulted key is never required

`SchemaSpec.Defaults` declares typed values. They are merged **under** every
`Source`, before the decode:

```
default  <  file  <  env  <  any later source
```

The default layer is placed first **structurally**, not by argument order.
There is no way to spell a load in which a default outranks a source, because a
default that could win is not a default. Everything after it is the caller's
order, so an explicit override is simply the last `Source`.

Being a layer rather than a post-decode fallback is what makes `timeout = 0`
survive: presence is decided while the operator's key is still a key. A
partially-supplied table keeps its unmentioned defaults, because the merge folds
key-by-key.

**A key may not be both required and defaulted.** The schema would fill the key
itself, so the presence pass would always find it and the requirement is a
clause that cannot fire — and a clause that cannot fire is worse than no clause,
because a reader takes it for protection. It is refused at construction with
`CONFIG_SCHEMA_INVALID`, in either order, and **at either nesting level**: a
default on `database` satisfies a requirement for `database.dsn`, and a default
on `database.dsn` creates the `database` table that satisfies a requirement for
`database`. Both are refused, by the same clause, for the same reason.

**Why two lists rather than one `{Key, Value, Required}` declaration.** A single
list would be more uniform and would make the contradiction visible at the
declaration site — and it would introduce an ambiguity the two-list form does
not have: `Value: nil` would mean both "no default" and "default to JSON null",
and only a second boolean could tell them apart. With two lists, everything in
`Defaults` **has** a default, including a nil one, and a required key has no
`Value` field to fill. The contradiction is refused either way; the ambiguity
only exists in one of them.

**Why `Rule` is singular.** `validation.All` already composes constraints. A
`Rules []Constraint` field would be a second spelling of composition the reader
has to learn, and the one that cannot express "all of these, but stop at the
first" the way the real combinators can.

### Decision 4 — everything that cannot work is refused at CONSTRUCTION

`NewSchema` compiles once and refuses, with `CONFIG_SCHEMA_INVALID` naming the
key and the clause:

| Refused | Why it is not a load-time problem |
|---|---|
| a key outside the grammar (empty, empty segment, leading/trailing separator) | it names a nesting level with no name, which the merged map cannot hold and the operator cannot type |
| a key naming no field of the target type | a defaulted typo silently never applies; a required typo fails every deployment for the wrong reason |
| the same key declared twice | a contradiction, not a last-wins |
| a key declared both as a value and as a table | ditto |
| a key both required and defaulted | Decision 3 |
| a default the JSON round trip cannot carry, or that does not decode into its field | a `"eighty"` for an int port is a source defect, discoverable without reading a file |
| a default that violates the constraint the schema declares for that key | see below |

The last one is ADR 0031's exact trap. A schema whose default sits outside the
bounds it also declares produces an invalid configuration on precisely the
deployment where nobody set the key — and reports it as the OPERATOR's fault.
The check decodes the defaults **alone** and runs the schema's own constraints
over them, scoped to the declared keys: a violation at a key nobody defaulted
belongs to the operator (every unsupplied key is still at its Go zero), a
violation at or under a key the schema itself filled belongs to the schema.

A tag the validation domain refuses surfaces **that domain's** error unchanged
(`INVALID_RULE` / `CONSTRAINT_MISCONFIGURED`), not a config code: its fields
already name the field, the rule and the clause, and relabelling would throw
away the diagnosis to gain a code the caller has to look up anyway.

A `nil` schema handed to `LoadSchema` is refused by name. An inert schema would
default nothing and check nothing while looking exactly like a working one —
ADR 0031 in its accepting form.

### Decision 5 — the schema FEEDS `validation`; it does not replace it, and it does not replace `Validator` either

`SchemaSpec.Rule` **is** a `validation.Constraint[T]`. `Schema.Check` returns a
`validation.Report`. They are the same types, not converted ones: a rule written
for an HTTP request body is the same value here. The schema compiles the
`validate` struct tags of `T` through `validation.Struct` — it does not read
tags itself and owns no rule vocabulary. There is no `min`, no `max`, no
`oneof`, no `pattern` anywhere in this domain, and adding one would be the
drift ADR 0046 exists to prevent.

`core/config.Validator` is likewise neither widened (ADR 0039) nor bypassed. A
struct that implements `Validate()` keeps being asked, **after** the schema's
constraints — a hand-written cross-field assertion has no useful answer while
the individual keys it reads are still invalid — and it may implement itself in
one line:

```go
func (c Conf) Validate() error { return appSchema.Check(c).Err() }
```

### Decision 6 — a message names the key and the rule, never the value

Every refusal in this domain carries keys and rule names and nothing else. A
configuration value is routinely a password, a token or a connection string, and
a start-up error is the one message in a service that reaches a log aggregator,
a terminal and a ticket at the same time. The keys are the schema author's own
literals, so echoing them is what makes a refusal actionable; the values are the
operator's, so echoing them is a leak.

This is ADR 0046's security property applied one layer up, and it has its own
tests on both halves: a construction refusal must not echo a default written in
source (it reaches a BUILD log), and a load refusal must not echo an operator's
value — including the value of the misspelled key it is refusing, which is
frequently the secret itself.

Key lists are clipped by **runes**, with the count carried separately so a
clipped message still says how much was clipped.

## Consequences / Semantics

**Codes.** Three serials in the block `core/config` already owns; no new `PP`
range, and `registry_ownership_external_test.go` is unchanged because
the `0.2.10.*` range (its table key is `0x00_02_0A_00`) is already mapped to
`internal/core/config`.

| Code | Name | When |
|---|---|---|
| `0.2.10.5` | `CONFIG_SCHEMA_INVALID` | `NewSchema` only — never a load outcome |
| `0.2.10.6` | `CONFIG_KEY_MISSING` | a load: required keys no source supplied |
| `0.2.10.7` | `CONFIG_UNKNOWN_KEY` | a load: keys the target cannot address |

**Both key failures travel together.** They are independent facts about the same
map, so an operator who fixes the missing keys only to be told about the typo on
the next restart has paid twice for one pass. When both occur they are joined
with `errors.Join`, and `errs.HasCode` walks `Unwrap() []error`, so both remain
matchable. When only one occurs it travels **alone**, unjoined, so
`errs.FieldsOf` reads its fields directly — `errs.FieldsOf` returns the first
`*Error` it finds in the tree, so wrapping a lone failure would cost the caller
a walk for nothing.

**Published shapes changed (ADR 0040 v0 licence, used and named).** The schema
work was not released, but the `config` facade was: `pkg/v1/config` gains
`Schema`, `SchemaSpec`, `Default`, `NewSchema`, `LoadSchema`, `KeyMissing` and
`UnknownKey`. Nothing existing changed shape — `Load`, `Source`, `Validator`,
`Watcher` and the four original sentinels are untouched, and a caller who never
builds a schema sees no difference at all.

**Naming.** `internal/service/config.SchemaValue[T]` is aliased as
`pkg/v1/config.Schema[T]`, and `NewSchemaValue` as `NewSchema` — the same
`XxxValue` → `Xxx` convention `metrics.StatsValue` → `Stats` and
`validation.ViolationValue` → `Violation` already use.

**Performance.** Measured in `internal/service/config/BENCH.md`. A schema
roughly doubles a load (6.4 µs → 13.7 µs, 16 → 32 allocations); the compile is
40 µs and 116 allocations and happens **once**, which is the reason for the
split; the unknown-key walk is 4 allocations and about 1.2 µs. A memory profile
showed all four of the key pass's allocations are `joinKey` building a nested
key's path, and that removing them is not worth a buffer aliased across a
recursion on a path that runs once per process — recorded there, with the
profile output, rather than acted on.

## What the schema does NOT do

Stated because each of these is a thing a reader may reasonably assume, and
assuming it wrongly costs a deployment.

- **It does not validate values.** No `min`, no `max`, no `oneof`, no `pattern`,
  no `email`/`url`/`uuid` (ADR 0046 refuses those by name). It composes
  `validation`.
- **It does not coerce types.** The decode is the ADR 0028 JSON round trip,
  unchanged. A default travels through exactly the same path a `Source`'s value
  does, which is why a default that cannot survive it is refused rather than
  specially handled.
- **It does not read anything.** A schema contributes a layer and two checks; it
  opens no file and reads no environment. `Schema.Source()` exposes the default
  layer as an ordinary `Source` for inspection — a `--show-config` flag, a diff
  between two releases — and hands out a fresh copy each time so a caller's
  mutation cannot reach the schema.
- **It does not map environment variable names.** That is `EnvSource`'s job, and
  its rule (prefix removed, lower-cased, underscores kept) is unchanged.
- **It cannot address a key inside a recursive type, a map, a slice, or a type
  that decodes itself.** The walk terminates at each; a key below one of them is
  refused at construction as naming no field. This is a stated limit, not an
  oversight — `time.Time` has exported fields and none of them are keys.
- **It does not make `Load` strict.** `Load` without a schema requires nothing,
  defaults nothing and drops an unaddressable key exactly as `encoding/json`
  does. The contract that existed before this ADR still exists.
- **It does not generate documentation, a JSON Schema, or a `--help`.** The
  declaration is Go, readable by Go, and a generator over it is a separate
  decision nobody has needed yet.
- **It does not redact.** It never *echoes* a value, which is a different and
  weaker promise: a `Schema.Check` report the caller renders itself is the
  caller's to handle, and the values in the decoded struct were never this
  domain's to hide.
- **It does not re-check on watch.** `Watcher` reports a change; re-running
  `LoadSchema` is the caller's call, and the ADR 0028 watcher contract is
  unchanged.

## Alternatives considered

**Widen `Load` with variadic options instead of adding `LoadSchema`.** Rejected:
the schema is a compiled artefact with its own construction failures, and
threading it through an option would put a `NewSchema` error at a `Load` call
site where it does not belong. Two entry points also keep "no schema" spellable,
which the ADR 0028 contract requires.

**Check required keys after the decode, with `validate:"required"`.** Rejected,
and it is the central decision: after the decode, absent and zero are the same
bytes. This is the same argument ADR 0046 makes when it tells a caller who needs
the distinction to declare a pointer — except that here the merged map still
holds the answer, so no pointer is needed and none should be demanded.

**Ignore unknown keys, and offer a `--strict` flag.** Rejected under ADR 0031:
the safe behaviour must be the zero value, and the dangerous one must be typed
out. A flag also puts the decision at the deployment, when the typo is the
author's vocabulary being missed.

**One declaration list with `{Key, Value, Required}`.** Rejected for the
`Value: nil` ambiguity described in Decision 3.

**Make the schema own its own rule vocabulary, so a config schema is
self-contained.** Rejected outright: two rule engines in one SDK is the drift
ADR 0046 was written to prevent, and "self-contained" here means "a second place
to look".

**A `map[string]FieldSpec` instead of slices.** Rejected: a map has no
declaration order, so a refusal could not name "the first offender" and a
missing-key report could not read in the order the author wrote it.

## Deferred

- **Emitting a JSON Schema / a `--help` from the declaration.** The declaration
  has everything needed; nobody has asked, and the output format is a decision
  of its own.
- **A `Diff(old, new T) []string` for reporting what a watch changed.** Belongs
  to the watcher story, not to the schema.
- **Marking a key secret so a `--show-config` redacts it.** Real, and it is a
  property of the KEY rather than of the schema's checks; it needs the rendering
  side to exist first.
- **Requiring a key conditionally ("`tls.key` when `tls.cert` is set").** It is
  expressible today as a cross-field `Rule`, which reports at the root. A
  first-class form would need a small predicate language, which is how a tag
  dialect turns into a language.

## References

- ADR 0028 — the `config` domain — `docs/adr/0028-sdk-config-domain.md`
- ADR 0046 — the `validation` domain — `docs/adr/0046-sdk-validation-domain.md`
- ADR 0031 — a zero value is never inert — `docs/adr/0031-policy-zero-values-are-never-inert.md`
- ADR 0039 / ADR 0040 — extending and changing published surfaces
- `internal/service/config/BENCH.md` — the numbers, and the optimisation recorded and refused
