# validation

Package `validation` implements the SDK's constraint engine over the
`internal/core/validation` port: the built-in constraints (`Required`, `AtLeast`,
`AtMost`, `Between`, `Length`, `Count`, `OneOf`, `Matches`), the combinators that
compose them (`All`, `First`, `Field`, `Each`, `Must`, `Check`), and the
struct-tag front end (`Struct`) that compiles a cached plan per type.

Two front ends, one contract. The programmatic path uses no reflection — each
descent is an accessor function. The tag path buys ergonomics with `reflect`
and caches the compiled plan, and the trade is measured rather than asserted:
see `BENCH.md`.

Collecting every violation is the default; `First` and `StructConfig.StopAtFirst`
really stop rather than filtering a full report. Every constraint that can be
misconfigured is refused at construction, and every dialect construct the SDK
declines to support is refused **by name**, with the fix in the message.

Facade: `pkg/v1/validation`. ADR 0046. See `CLAUDE.md`.
