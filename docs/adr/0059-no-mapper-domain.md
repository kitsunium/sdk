# ADR 0059 — there is no `mapper` domain: a projection is a type, not a tag

- **Status**: Accepted — and what it accepts is a **refusal**. No `core/mapper`,
  no `service/mapper`, no `pkg/v1/mapper` is built. The reserved code slots
  `0.2.28` and `0.3.58` are **released**, not allocated.
- **Date**: 2026-09-10
- **Deciders**: SDK maintainers
- **Related**: [ADR 0003](0003-sdk-codec-package.md) (the `Codec` contract this
  ADR was justified against), [ADR 0046](0046-sdk-validation-domain.md) (the
  struct-tag front end and its cached plan — the mechanism a mapper would have
  copied), [ADR 0013](0013-sdk-crypto-domain.md) (`jwk`'s public/private
  projection), [ADR 0060](0060-sdk-health-domain.md) (the probe body's closed
  wire shape), [ADR 0057](0057-sdk-authz-domain.md) (there is no policy DSL),
  [ADR 0031](0031-policy-zero-values-are-never-inert.md) (a default is never
  inert — here, never the leaky one), [ADR 0030](0030-stdout-is-a-protocol-channel.md)
  (the zero value must not be the dangerous one)

## Context

### 1. The ticket, and the sentence that carried it

The backlog entry (`B15. mapper`, in the feature-ideation note) reads, verbatim:

> `codec` s'arrête aux octets ; il manque struct ↔ map, groupes de
> sérialisation, renommage, champs ignorés.
> *("`codec` stops at bytes; struct ↔ map, serialisation groups, renaming and
> ignored fields are missing.")*

That is one premise and four claimed gaps. The premise is checkable, and it was
checked before anything was designed.

### 2. The premise is false

`internal/core/codec/codec_interface.go` declares:

```go
type Codec interface {
	Name() string
	MIMETypes() []string
	Extensions() []string
	Marshal(v any) (data []byte, err error)
	Unmarshal(data []byte, v any) (err error)
}
```

`Marshal` takes **a Go value**. `Unmarshal` fills **a Go value**. The domain is
object-oriented on both ends; bytes are its *output*, not its input vocabulary.
The sibling extensions do not change this — `Appender.Append(dst []byte, v any)`
still takes the value, and `StreamingCodec`'s `Encoder.Encode(v any)` /
`Decoder.Decode(v any)` do too.

"`codec` stops at bytes" describes a codec this SDK does not have. **A domain
built on that sentence would be built on nothing.** Three of the four claimed
gaps fall with it:

| Claimed gap | Where it already lives |
|---|---|
| renaming | `json:"name"` — stdlib, and already the SDK's naming vocabulary (below) |
| ignored fields | `json:"-"` — stdlib |
| map → struct | `internal/service/config/load.go`, `decodeInto` — **twelve lines**, a `json.Marshal` / `json.Unmarshal` round trip, in production and load-bearing today |

The fourth — **struct → map** — has no in-tree implementation, and no in-tree
caller either. `authz` builds its fact bag from explicit typed `AttrValue`s
(`internal/core/authz/request_value.go`) and *drops* an invalid one, because
`Attr(key) (AttrValue, ok)` is "the whole reason the attribute bag is not
exported"; `logger` went the other way entirely and built a discriminated union
(`internal/core/logger/value.go`) specifically to "skip the cost of reflection
at format time". Neither wants a `map[string]any`, and both say why in their own
doc comments.

That leaves exactly one candidate worth an ADR.

### 3. The one real candidate: groups

`encoding/json` genuinely has no notion of a serialisation group. Exposing a
different subset of one struct's fields per context — public API vs internal,
list vs detail, v1 vs v2 — is a real recurring problem, it is why Symfony has
serializer groups and Jackson has `@JsonView`, and the SDK cannot answer it with
a struct tag today.

So the question is not "does the gap exist". It does. The question is **whether
this SDK's answer to it should be a group tag** — and the SDK has already
answered that question three times, in production, without anybody framing it as
a domain.

## Decision

**No `mapper` domain is built.** A projection of a struct is expressed as
**another type** plus a **named method**, never as a runtime group key read from
a tag.

The three reasons, in the order they were established.

### D1. The SDK has already solved this three times, and never with a group

**`internal/service/crypto/jwk`** — the exact use case, with a real security
property. Its package comment states the decision outright:

> Serialising a key is the operation that undoes `core/crypto.Key`'s redaction
> […] So the two directions are not a boolean argument — they are two
> differently named methods, and the one the language reaches for on its own
> (`MarshalJSON`, i.e. plain `json.Marshal`) is the safe one. Emitting `"d"` or
> `"k"` requires typing `MarshalPrivate` at the call site, **where a reviewer
> reads it**.

And, on `MarshalJSON`: *"There is no flag, no option struct and no context value
that flips it — the private path has its own name."* A group key is precisely
the flag that paragraph refuses, spelled as a string.

**`internal/service/health`** — the internal report vs the probe body.
`body.go`'s `checkValue` carries a closed comment that is itself the rule:

> Nothing else may be added: the raw error, the Private half and the fields are
> the three things this type exists to keep out.

**`internal/kernel/errs`** — SDK rule 4, the `Public` / `Private` split. The
SDK's oldest answer to "which half of this value may leave the process" is *two
typed fields*, not one field with a context-dependent visibility.

### D2. A group mapper would have replaced **none** of the three

This is the measurement that settles it, and it is not a performance one. Each
of the three does something a field-subset cannot express:

- **`jwk` refuses.** `publicWire()` returns `NoPublicForm` for a symmetric key —
  an `oct` JWK *is* its secret, so there is no public projection of one. A group
  tag has no way to say "this type has no projection in this group at all"; the
  best it can do is emit a key-shaped object with no key, which the code calls
  out as "worse than an error". Symmetrically, `privateWire()` refuses a
  public-only key rather than silently downgrading to the public output.
- **`health` reshapes.** `HandlerConfig.Detail` does not hide fields; it decides
  whether `Checks` is *computed at all*, and each entry is derived — `TookMs`
  from a `time.Duration`, `Reason` from `errs.PublicOf(err)` via `publicReason`,
  which is documented as "the ONLY path by which anything derived from a check's
  error reaches the wire".
- **`token` passes bytes through.** `encodeClaims` assembles its member map with
  `putString` / `putTime` and copies private claims as verbatim
  `json.RawMessage`. There is no struct to reflect over.

A domain that would have deleted no existing code, and whose three closest
in-tree relatives each need a capability it does not have, is not filling a gap.
It is adding a fourth way to do something the SDK does three ways already —
each of which is stricter.

### D3. Measured: it is slower, allocates more, **and** it is the one that leaks

Both halves were measured rather than asserted, against the *strongest* form of
the proposal — a plan compiled once per `(type, group)` and cached, which is
exactly the mechanism [ADR 0046](0046-sdk-validation-domain.md) uses and which
the ticket asked to follow.

**Reproducibility envelope** — AMD EPYC 7351P (8 vCPU), 15 GiB, Linux
6.12.101+deb13-amd64, Go 1.27.1, `206e835`, `-benchtime=400ms -count=3`, median
of three. The harness is a throwaway, deliberately not committed: it exists to
answer one question, and shipping it would be shipping the mapper's skeleton.

*Producing one public JSON body from a 5-field struct:*

| Approach | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Closed wire struct + `json.Marshal` (**what the SDK does today**) | **705** | **96** | **3** |
| Group tag, plan compiled once per (type, group) and cached, then encode | 2 455 | 568 | 11 |
| Group tag, plan handed in — no cache lookup at all, the *floor* for any reflect mapper | 2 261 | 568 | 11 |
| Group tag, recompiled per call (the naive version) | 3 878 | 680 | 16 |

The cached plan works — it buys 1.58× over recompiling, consistent with ADR
0046's finding. It cannot close the gap, because the gap is **structural**: a
mapper must box every field into `any` (that is the 432 B before anything is
encoded) and then encode a `map[string]any` — sorted keys, interface dispatch
per value — instead of a static struct whose encoder `encoding/json` compiles
once and reuses. Even at its floor it is **3.2× slower and allocates 5.9× the
bytes** of the code it would replace.

Stated plainly: **when the destination is the wire, the map is not a step toward
it, it is a detour away from it.**

For completeness, and honestly: struct → map *as an end product* — if a caller
genuinely needs a `map[string]any` and not bytes — is where reflection does win.
A cached-plan mapper costs 481 ns / 432 B / 3 allocs against 7 771 ns / 1 233 B /
25 allocs for the `json.Marshal`+`json.Unmarshal` round trip: **16× faster**.
That number is real. It is also the number for a caller this repository does not
have (§Context.2), which is why it does not carry the decision — see §Deferred
for what would change that.

### D4. The safety half, demonstrated

The ticket asked which default is right for a field that names no group —
omitted, or zero. The honest answer is that **both defaults are wrong**, and the
experiment says so rather than the ADR asserting it. A developer adds a field to
an existing struct and does not think about the `groups` tag:

```
permissive  ("no tag => in every group")
  BEFORE: public projection = [id]
  AFTER : public projection = [id passwordResetToken]
          -> LEAK: the untagged field reached the public projection,
             value present = "SECRET"

restrictive ("no tag => in no group")
  AFTER : admin projection  = [email id]
          -> SILENT DROP: the admin projection lost a field nobody was told about.

closed wire struct
  AFTER : {"id":"u1"}
```

The permissive default is a data leak. The restrictive default is not safe
either, it is merely *quiet* — it silently narrows a projection that a caller
believes is complete, which is how a field stops being audited without anyone
choosing that. ADR 0031 requires a zero value that is either a safe default or
an explicit refusal; here **neither branch qualifies**, and a domain whose zero
value cannot be made correct should not have a zero value.

The closed wire struct's output is unchanged, and *cannot* change: adding a
field to the source type gives it nowhere to go. The projection is closed by
construction rather than by remembering a tag — the same shape of argument
[ADR 0045](0045-sdk-session-domain.md) makes for session fixation ("prevented by
the ABSENCE of any other way to spell a login") and
[ADR 0042](0042-sdk-token-domain.md) makes for algorithm confusion ("a call that
does not compile").

### D5. And it would have been a DSL

[ADR 0057](0057-sdk-authz-domain.md) refuses a policy DSL because it is "a
second, weaker language inside a program that already has one", it is untyped,
and it puts a parser on the authorization path. `groups:"public,admin"` is that
same object: a comma-separated, string-keyed, untyped, runtime-evaluated
projection language embedded in struct tags — deciding **which fields leave the
process**. If the SDK will not accept a DSL for who may read a resource, it does
not get to accept one for what they read.

Note also that the group key would have been a *second* string vocabulary. The
SDK already has exactly one field-naming vocabulary and a stated reason for it —
`internal/service/validation/compile.go`:

> It is `json` rather than the Go field name because of a fact about this SDK,
> not a preference: `internal/service/config.Load` decodes every format […]
> through a `json.Marshal`/`json.Unmarshal` round trip, so the json tag is
> literally the key the operator wrote in their file.

A mapper would reuse `json` and add nothing to naming, or invent a third
vocabulary. Neither is worth a domain.

## Consequences / Semantics

- **Nothing ships.** No package, no error codes, no public surface, no README.
- **The reserved slots are released.** `0.2.28` (`0x00_02_1C_*`) and `0.3.58`
  (`0x00_03_3A_*`) were held for this ticket and are **never added** to
  `codeRangeOwners` in `internal/kernel/errs/registry_ownership_external_test.go`.
  They return to the free pool for the next domain. The ownership audit is
  unaffected — it judges declarations that exist, and there are none.
- **What a caller writes instead** — the pattern, in full, as the three in-tree
  precedents already spell it:

  ```go
  // The domain type. It is not a wire type and never leaves the process.
  type User struct {
      ID                 string
      Email              string
      PasswordResetToken string
  }

  // One closed wire type per projection. Adding a field to User gives it
  // nowhere to go here — that is the whole point.
  type userPublic struct {
      ID string `json:"id"`
  }

  type userAdmin struct {
      ID    string `json:"id"`
      Email string `json:"email"`
  }

  // One named method per projection. The name is what a reviewer reads at the
  // call site, and the safe one is what the language reaches for on its own.
  func (u User) MarshalPublic() ([]byte, error) {
      return codec.Marshal(codec.JSON, userPublic{ID: u.ID})
  }

  func (u User) MarshalAdmin() ([]byte, error) {
      return codec.Marshal(codec.JSON, userAdmin{ID: u.ID, Email: u.Email})
  }

  // json.Marshal — and any generic encoder, and any struct that embeds a User —
  // takes the public path. There is no flag that flips it.
  func (u User) MarshalJSON() ([]byte, error) { return u.MarshalPublic() }
  ```

  The projection travels through `pkg/v1/codec` exactly as any other value does,
  so it picks up all 24 formats for free. When a projection must *refuse* rather
  than narrow — `jwk`'s `oct` case — the method returns a typed `errs` sentinel,
  which a group tag could not have expressed at all.

- **The one thing a caller loses** is the N×M boilerplate, and it is a real
  cost: N entities × M contexts means N×M wire structs. The SDK accepts that
  cost knowingly. Each of those structs is a place a reviewer sees the field
  list, and each of them is checked by the compiler; the alternative moves the
  same decision into a tag nobody diffs. It is the same trade ADR 0057 makes
  when it writes a grant table as a Go slice.

## Breaking changes

None. Nothing existed to break — this ADR is the decision not to create it.

## Alternatives considered

**Build it anyway, restrictive-by-default.** The safer of the two defaults, and
still refused: §D4 shows it silently narrows projections, §D3 shows it is 3.2×
slower than the code it replaces even at its floor, and §D2 shows it would have
replaced none of the three in-tree cases. Safer than the leak is not the same as
safe.

**Build only struct ↔ map, no groups.** This is the honest minimum, and it is
`decodeInto`: twelve lines that already exist for the direction that has a
caller, and two stdlib calls for the direction that does not. Rule 5 ("no empty
stub files / dirs") reaches this: a domain whose entire content is a helper
around two stdlib calls has no vocabulary, so it has no boundary, so it grows
without end — which is the argument the same ideation note already makes when it
refuses a `samber/lo`-style catch-all.

**Build it as a codec extension interface**, the way `multipart` got
`BoundaryCodec` in [ADR 0037](0037-sdk-codec-multipart.md). Rejected for the
reason ADR 0037 itself gives: that precedent exists because the *format* needed
something `Marshal(v any)` could not carry — the boundary is in the
`Content-Type` header. A group is not a property of the format. Every one of the
24 codecs would have to honour it identically, and the extension would be a
per-codec reimplementation of the same reflection.

**Generate the wire structs.** A code generator emitting `userPublic` /
`userAdmin` from a tag keeps the compile-time guarantee and deletes the
boilerplate — the one alternative that does not trade safety for convenience.
It is out of scope here (this ADR is about a *runtime* domain, and the SDK ships
no generator today beyond `gomarkdoc` and `gazelle`), and it is recorded in
§Deferred rather than dismissed.

**Wait for `encoding/json/v2`.** Its `MarshalerTo` / options model would change
the *cost* of a mapper, not its safety argument. §D4 does not depend on the
encoder.

## Deferred

Three things would reopen this, and naming them is what keeps a refusal from
becoming dogma:

1. **A real in-tree consumer of struct → map.** Today there is none (§Context.2).
   If one appears — and the plausible one is a future `queue` or `i18n` needing
   a generic payload bag — the 16× number in §D3 becomes load-bearing and this
   ADR should be revisited. Note it would be a *struct → map* need, not a
   *groups* need; the two are separable and only the first has a measured win.
2. **A wire-struct generator.** If the SDK ever grows `go:generate` tooling
   beyond docs, emitting projection structs from a tag is the version of this
   feature that keeps the compile-time guarantee. That is a build-time decision
   and would get its own ADR.
3. **Evidence the boilerplate is actually hurting.** The N×M cost is accepted
   above on principle. If a consumer reports it concretely — with the count —
   that is data this ADR did not have.

What is **not** deferred, and would need to overturn §D4 rather than extend it:
a runtime, tag-driven, string-keyed group mechanism deciding which fields leave
the process.

## References

- `internal/core/codec/codec_interface.go` — the `Codec` contract the premise
  misdescribed
- `internal/service/config/load.go` (`decodeInto`) — map → struct, in-tree,
  twelve lines
- `internal/service/crypto/jwk/marshal.go` — the public/private projection, and
  the paragraph that refuses the flag
- `internal/service/health/body.go` — the closed wire shape and what it exists
  to keep out
- `internal/service/token/claims_codec.go` (`encodeClaims`) — projection by
  explicit assembly
- `internal/core/logger/value.go` — the discriminated union built to avoid
  reflection and `any`
- `internal/service/validation/compile.go` — the `json`-tag naming rule and its
  stated reason
- [ADR 0046](0046-sdk-validation-domain.md) §the struct-tag front end — the
  cached-plan mechanism this ADR measured and still refused
- [ADR 0057](0057-sdk-authz-domain.md) §Why not a policy DSL
- [ADR 0031](0031-policy-zero-values-are-never-inert.md) — a zero value is a
  safe default or an explicit refusal
