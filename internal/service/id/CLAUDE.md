# internal/service/id/

## Purpose

Concrete `core/id.Generator` implementations over stdlib `crypto/rand` + the
kernel `clock`. Blank-importing self-registers every scheme that needs no
configuration; snowflake, NanoID and TypeID also offer explicit constructors.
**No vendor deps** (stdlib only). ADR 0024. Code range: `0.3.39.*`.

## Contents

| File | Scheme | Notes |
|---|---|---|
| `common.go` | — | shared consts + `readRandom`/`formatUUID`/`parseUUID`/`setUUIDBits`/`putUint48BE` + the Crockford base32 codec (`crockford32Encode`/`crockford32Decode`) shared by ULID and TypeID |
| `uuidv4.go` | `uuidv4` | RFC 9562 §5.4, fully random |
| `uuidv7.go` | `uuidv7` | RFC 9562 §5.7, 48-bit ms prefix (k-sortable) |
| `ulid.go` | `ulid` | 48-bit ms + 80-bit random, UPPERCASE Crockford base32 (26 chars) |
| `snowflake.go` | `snowflake` | stateful: 41-bit ms + 10-bit node + 12-bit seq → decimal; `NewSnowflake(node)` |
| `nanoid.go` | `nanoid` | 21 URL-safe chars by default, no timestamp; `NewNanoID(size)`; rejection sampling, never `%` |
| `ksuid.go` | `ksuid` | 32-bit second prefix + 128-bit payload → 27 base62 chars; decodes via `ParseKSUID` |
| `typeid.go` | `typeid` **(unregistered)** | type prefix + `_` + UUIDv7 in LOWERCASE Crockford base32; `NewTypeID(prefix)`, `ParseTypeID`, `FormatTypeID` |
| `codes.go` | — | `0.3.39.*` (ID_ENTROPY_FAILED, ID_CLOCK_BACKWARDS, ID_CLOCK_STALLED, ID_INVALID_SIZE, ID_INVALID_PREFIX, ID_MALFORMED, ID_TIMESTAMP_RANGE) |
| `errors.go` | — | sentinels |

## Conventions

- **Registration via `var X = id.Register(...)`, never `init()`.**
- Stateless singletons (uuidv4/v7/ulid/nanoid/ksuid) ; snowflake is per-instance
  stateful (mutex + sequence), with a default-node singleton + `NewSnowflake(node)`.
- Entropy failures wrap `crypto/rand` via `errs.Wrap` (ID_ENTROPY_FAILED).
- Parse refusals carry a `rule` field naming the clause that fired
  (`length` / `alphabet` / `overflow` / `charset` / …) and **never echo the
  input** — these errors are logged, and the input is unbounded caller data.
- Cross-OS: 100 % portable (crypto/rand, time, hashing) — no OS-specific code.

## Snowflake clock safety

`New` holds a mutex for the whole same-millisecond overflow wait, so that wait
is bounded in every direction the clock can misbehave:

| Clock behaviour | Result |
|---|---|
| advances past the exhausted ms | normal exit — the id is issued |
| regresses below it | `ID_CLOCK_BACKWARDS` immediately (waiting can never fix it) |
| stalls at it | keeps spinning up to `maxTillNextSpins`, then `ID_CLOCK_STALLED` |

The stall bound is a **spin count, not a duration**: the component that may be
broken is the clock itself, so a deadline derived from it could never fire.
On the failure path the sequence is restored to `maxSeq` — leaving it at the
wrapped `0` would let a retry inside the same millisecond reissue a used
sequence.

## NanoID and the modulo bias

`randomFromAlphabet` masks each random byte down to the smallest `2^k-1` that
spans the alphabet and **rejects** any masked value landing past the last
symbol, redrawing for that position. It never takes a remainder.

`alphabet[b%len(alphabet)]` is the defect every reimplementation ships. Unless
the alphabet size divides the byte range exactly, the symbols at the start of
the alphabet are reachable one extra way and so appear more often — in every
identifier, forever. Nothing observable breaks: the ids still look random, never
repeat, and pass a uniqueness test. Only the distribution is skewed, and the
effective entropy is below what the length claims.

The shipped 64-symbol alphabet makes the mask exactly 6 bits, so **no draw is
ever rejected** on the production path. The rejection is not dead weight: it is
what keeps the sampler correct for any other alphabet size, and
`Test_randomFromAlphabet` drives it with 62-, 33- and 3-symbol alphabets so the
branch is executed rather than assumed. That test also asserts a uniform
distribution to within 15 %, which is comfortably under the ~21 % skew a naive
`%` produces over 62 symbols and roughly seven standard deviations away from the
uniform expectation — it separates the two mechanisms without being flaky.

## Why TypeID is not registered

Every other scheme here is reachable through `core/id.New`. TypeID is not, and
`New("typeid")` returning `UNKNOWN_SCHEME` is the intended answer.

A TypeID's prefix names the caller's entity. There is no prefix the SDK could
publish a singleton under without either inventing their domain vocabulary or
minting `_01h2…` — both are exactly the silent degradation **ADR 0031** exists
to forbid. So the scheme is reachable only through `NewTypeID(prefix)`, which
refuses an invalid prefix once, at construction, instead of per identifier.

The same reasoning splits NanoID the other way: 21 is the format's *published*
default, not a number invented here, so the zero-value struct resolves to it
(`length()`), while `NewNanoID(0)` — an explicit argument, an assertion the
caller means it — is refused.

## Decoding

`ksuid` and `typeid` are the two schemes that read back, and both are tested for
round-trip in **both** directions against fixtures from their reference
implementations, not only against this package's own output:

| Direction | KSUID | TypeID |
|---|---|---|
| decode | `ParseKSUID` → issue time + payload | `ParseTypeID` → prefix + dashed UUID |
| encode | `base62Encode` (internal) | `FormatTypeID` — relabels an existing UUID |

Two rejections are load-bearing and easy to omit:

- **KSUID magnitude.** `62^27 ≈ 2^160.7`, so about a third of well-formed-looking
  27-character strings encode a number 20 bytes cannot hold. Truncating them
  would make two distinct strings decode to the same identifier.
- **TypeID pad bits.** 26 base32 characters carry 130 bits for a 128-bit value,
  so a first character above `'7'` is over-wide. Accepting it would silently
  drop the two high bits, and the decoder would stop being the encoder's inverse
  without ever erroring.

`ParseTypeID` splits the suffix off by **width**, counting back from the end —
not on the first or last `_`. A prefix may legitimately contain underscores
(`user_account_01h2…`) while the suffix alphabet never does.

`FormatTypeID` deliberately does **not** enforce the UUID version. `New` only
ever mints v7, but refusing to label a v4 that already exists in a caller's
database would defeat the one job that function has.

## Do NOT

- Add an `init()`. Use the package-level `var = id.Register(...)`.
- Claim zero-alloc: string rendering allocates by design.
- Register a `typeid` generator with a default prefix — see above.
- Replace the NanoID rejection loop with `%`, however tempting the arithmetic
  looks for a 64-symbol alphabet.

## Verification

```
bazel test --config=race //internal/service/id:id_test
```
