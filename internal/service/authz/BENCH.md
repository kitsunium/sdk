<!-- generated from internal/service/authz/authz_bench_test.go — refresh with `cd internal/service && GOWORK=off go test -run '^$' -bench=. -benchmem -count=5 ./authz/` -->
# Benchmarks — `internal/service/authz`

## Reproducibility envelope

> **Numbers vary across machines.** This report stamps the box that produced
> them so cross-machine deltas can be evaluated honestly.

| Dimension | Value |
|---|---|
| CPU               | AMD EPYC 7351P 16-Core, 8 vCPU visible |
| RAM               | 15 GiB |
| OS / kernel       | Linux 6.12.101+deb13-amd64 (Debian GNU/Linux 13, trixie) |
| Architecture      | amd64 |
| Go toolchain      | go1.27.1 linux/amd64 |
| Git branch        | `agent-a4945e29c0c2717a3` |
| Git commit        | `e01714c` (the tree this domain was added to) |
| Generated (UTC)   | 2026-09-10 |
| Bench wall-clock  | `-benchtime=1s -count=5`, median quoted |

## What is being measured

An authorization check sits on the path of **every** request a service serves,
before the work the request came for. A domain that answers "may this" in
nanoseconds is a domain nobody has to route around; one that allocates per
check turns every request into GC pressure. The claim being measured is that
the evaluation path allocates **nothing at all**, and that it is independent of
the size of the grant table.

The fixture is the shape a service actually wires: eight roles × four
permissions (33 grants over 9 distinct permissions) under `NewRBAC`, plus a
two-rule `NewABAC` — one `Deny` rule on a flag, one `Allow` rule on ownership —
composed under `DenyOverrides`. The request carries four attributes, one of
each kind.

## Results

| Benchmark | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| `NewRequest` — build the question, 4 attributes | **593** | 976 | 3 |
| `RBACAllow` — grant hit, 33-entry table | **67.9** | 0 | 0 |
| `RBACAbstain` — no role confers it | **58.9** | 0 | 0 |
| `ABACAllow` — two matching rules, one fires | **117** | 0 | 0 |
| `CheckAllow` — RBAC + ABAC + closure, permitted | **210** | 0 | 0 |
| `CheckDenied` — the same, refused | **423** | 400 | 2 |
| `PerRequest` — build **and** answer: what one request pays | **847** | 976 | 3 |

**Read the spreads, not just the medians.** This box is shared with other
builds, and the two families of benchmark here have very different noise
floors. The allocation-free evaluation benchmarks are tight — `CheckAllow` held
within 0.3 % of its median over five runs, `ABACAllow` within 5 % — so a
percent-level difference between two variants of the evaluation path is real.
Anything that allocates is GC-dominated and much wider: `NewRequest` ±8 % and
`PerRequest` 799–1074 ns around a median of 847. A sub-10 % difference in
*those* numbers is not a result, and the §`AttrValue` section below turns on
exactly that distinction.

## How to read this

- **The permitted path allocates nothing.** 0 B/op across `RBACAllow`,
  `RBACAbstain`, `ABACAllow` and the full `CheckAllow` composition. That is not
  an accident of the fixture: `AttrValue.Contains` scans the attribute's
  backing slice in place rather than going through `StringsValue`, which
  clones — the clone exists so an attribute stays immutable when it leaves the
  domain, and the hot path deliberately never asks for one. A composition
  builds no intermediate result either, because the fold is two locals.

- **210 ns is the whole check.** RBAC (68) + ABAC (117) + the fold and the
  closure (~25). At that cost there is no reason to cache an authorization
  decision, and caching one is how a revoked role keeps working for five
  minutes.

- **A request pays ~847 ns end to end**, and about 70 % of that is building the
  question rather than answering it (594 + 210 ≈ 804, which is the sum
  `PerRequest` measures directly). The authorization *decision* is the cheap
  part of authorization.

- **Refusing costs 400 B and 2 allocations, and it should.** The refusal is an
  `*errs.Error` carrying six diagnostic fields — outcome, subject, action,
  resource, and the cause's code and reason. That is the operator's only view
  of a denial, since the public sentence deliberately says nothing (ADR 0057
  §D4), so the fields are the feature and not overhead to trim. The asymmetry
  is also the right way round: the permitted path is the one every request
  takes.

- **An abstention is cheaper than a grant** (59 vs 68 ns) because it is one map
  lookup and no scan — the inverted `permission → roles` index means a
  permission nobody grants is answered by a miss. The common request in a real
  system is exactly that one.

- **The table's size does not reach the request.** `indexGrants` inverts
  `role → permissions` into `permission → roles` once, at construction, so an
  evaluation costs one map lookup plus a scan of the roles that confer *that*
  permission — one or two in any realistic table — rather than a walk of the
  subject's roles against every grant. Growing the table grows the map, not the
  check.

## Where the time goes — `go tool pprof`, not a guess

`go test -bench='^BenchmarkCheckAllow$' -benchtime=3s -cpuprofile`, then
`pprof -top`. Total samples 3.65 s:

| Node | flat | cum |
|---|---:|---:|
| `authz.lookupAttr` | 21.6 % | **34.8 %** |
| `runtime/maps.memHashAES` | 12.6 % | 12.6 % |
| `runtime.mapaccess2_faststr` (cum, under `lookupAttr` + `Attr`) | 3.6 % | 16.4 % |
| `authz.evaluateRules` | 2.5 % | 46.6 % |
| `authz.evaluateRBAC` | 4.9 % | 28.8 % |

**The hot path is attribute lookup, and attribute lookup is map hashing plus
one struct copy.** `pprof -list lookupAttr` puts it on a single line:

```
     790ms      1.27s (flat, cum) 34.79% of Total
      70ms       70ms    160:func lookupAttr(...) (attr coreauthz.AttrValue, err error) {
     580ms      1.06s    162:	found, ok := request.Attr(key)
     130ms      130ms    174:	return found, nil
```

Line 162 is one map read; its 1.06 s cum is the hash plus the probe, and the
**580 ms flat is the 80-byte `AttrValue` being copied out of the map**. Line
174 is the same value copied out to the caller (130 ms), and line 160 is the
frame that holds the return slot (70 ms). Together ≈ 780 ms of 3.65 s — about a
**fifth of the check** is moving `AttrValue` around.

### The 80-byte `AttrValue` was measured, and the first answer was wrong

That fifth is exactly what `KTN-VAR-BIGSTRUCT` warns about (80 B, threshold
64 B), so the obvious shrink was **tried** rather than argued about: fold
`flag bool` into the `int64` payload and reorder the members — the shape
`internal/core/metrics.AttrValue` already uses. It takes the struct to 72 B.

| | `AttrValue` | `ABACAllow` | `CheckAllow` | `CheckDenied` | `NewRequest` | `PerRequest` |
|---|---:|---:|---:|---:|---:|---:|
| as shipped | 80 B | **117.3** | **209.8** | **422.6** | **593** / 976 B | **847** / 976 B |
| folded + reordered | 72 B | 135.3 | 225.3 | 454.8 | 542 / 848 B | 1079 / 848 B |
| delta | −8 B | **+15.3 %** | **+7.4 %** | **+7.6 %** | **−8.6 %** | *within noise* |

**It is a trade, not a win, and the first measurement of it was wrong.** The
initial run of this experiment reported `NewRequest` at +52 %, which would have
made the decision obvious and would have been an artifact: that run used a
`map[bool]int64{…}[value]` literal instead of a branch, and it was taken while
another build was on the box. Re-measured with a branch on a quiet box, the
folded variant is **faster** on construction and allocates 128 B less. The
lesson is the one the profile already implied — *record the spread before
believing a delta* — and it is why the §Results table above leads with noise
floors.

**Decision: keep 80 bytes.** Three reasons, in order of weight:

1. **The only clean signal favours it.** The allocation-free evaluation
   benchmarks are the tight ones (`CheckAllow` ±0.3 %), and the folded variant
   is reliably 7–15 % worse on every one of them. 80 is five whole 16-byte
   moves and 72 is not, so the `memmove` out of the map gets slower despite
   copying less — and the penalty scales with the number of attribute reads,
   which is why two-lookup `ABACAllow` (+15 %) suffers twice what one-lookup
   `CheckAllow` (+7 %) does.
2. **There is no measured end-to-end win.** `PerRequest` — the only benchmark
   that is what a request actually pays — puts the two within each other's
   spread (847 vs 1079 median, but 799–1074 against 1031–1460). The
   construction saving and the evaluation cost roughly cancel, and the residue
   is smaller than the noise.
3. **It does not even silence the rule.** 72 B is still over the 64 B
   threshold, so the exclusion would be needed either way.

Getting under 64 B would mean deleting the `key` member — the thing that lets a
caller write one flat `NewRequest(subj, act, res, attrs...)` list instead of
building a map at the call site. Not worth it against an evaluation path that
already allocates zero. Two other shapes were rejected on mechanics rather than
taste, without benching: a **pointer receiver** cannot help, because Go map
values are not addressable and `r.attrs[k].Contains(x)` would not compile; and
`map[string]*AttrValue` trades one hot-path copy for one heap allocation **per
attribute** on the construction path, which is already the allocating half.

The `.ktn-linter.yaml` exclusion for `KTN-VAR-BIGSTRUCT` cites this section.

### The three allocations, named

`go test -bench='^BenchmarkNewRequest$' -memprofile`, then
`pprof -sample_index=alloc_space` and `-sample_index=alloc_objects`:

| Site | B/op | allocs/op |
|---|---:|---:|
| `coreauthz.NewRequestValue` — the attribute map + its group backing | ~936 (96.8 %) | 2 |
| `slices.Clone` inside `AttrStrings` — the roles set | ~32 (3.1 %) | 1 |

**`NewRequest` is the caller's allocation, not the SDK's hot path.** The map
dominates because a Go swiss map sizes a whole group: four 80-byte attributes
become eight slots of key + value, which is where ~936 B comes from — not from
the attributes themselves (4 × 80 = 320 B). It is paid once per request,
against which 210 ns of evaluation is the cheap half.

The one clone is the price of immutability: `AttrStrings` copies the caller's
slice so a later `append` cannot rewrite an attribute a live evaluation is
reading. A caller with no set attributes pays neither — a request built with no
attributes at all allocates only the value, because the map is created lazily,
on the first attribute that survives the constructor's filter.
