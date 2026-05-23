<!--
  Per-package "Use cases" — INTERACTIVE tabs (HTML). The narrative
  "Goals" + "What's shipped" tables live in the Go doc comment of
  accessors.go (so pkg.go.dev + README.md show them too); only the
  HTML tabs need a hand-authored file because raw HTML inside a Go
  doc comment renders as literal text on pkg.go.dev.

  Code blocks inside each tabpanel use markdown fences with blank
  lines around them so astro-expressive-code processes them at build
  time (Shiki github-dark + copy-icon button — same as Quick start).
-->

## Use cases

Pick the matcher / accessor that fits the routing you need.

<div class="tabs" data-tabs>
<div class="tab-strip" role="tablist">
<button type="button" role="tab" id="errs-hascode-btn" aria-controls="errs-hascode" aria-selected="true" tabindex="0" class="active">Match a specific code</button>
<button type="button" role="tab" id="errs-hasany-btn" aria-controls="errs-hasany" aria-selected="false" tabindex="-1">Match a set of codes</button>
<button type="button" role="tab" id="errs-hasreason-btn" aria-controls="errs-hasreason" aria-selected="false" tabindex="-1">Match by symbolic reason</button>
<button type="button" role="tab" id="errs-prefix-btn" aria-controls="errs-prefix" aria-selected="false" tabindex="-1">Match a code range</button>
<button type="button" role="tab" id="errs-render-btn" aria-controls="errs-render" aria-selected="false" tabindex="-1">Render safely</button>
</div>

<div role="tabpanel" id="errs-hascode" aria-labelledby="errs-hascode-btn">

**Best for:** branching on one specific failure mode — the precise `MM.LL.PP.SS` coordinate. Walks the `Unwrap()` chain so wrapped errors are caught.

```go
if errs.HasCode(err, codec.CodeUnknownFormat) {
    // The caller asked for a Format we never registered.
    return fallback
}
```

</div>

<div role="tabpanel" id="errs-hasany" aria-labelledby="errs-hasany-btn" hidden>

**Best for:** retry / circuit-breaker policies that branch on a SET of failure modes ("retry on these 5 codes"). Variadic OR — saves the `HasCode(err,a) || HasCode(err,b) || …` chain.

```go
retryable := errs.HasAnyCode(err,
    codec.CodeStreamingUnsupported,
    logger.CodeWriterRequired,
    logger.CodeSinkConfigRequired,
)
if retryable {
    return backoff.Retry(ctx, do)
}

// String-keyed sibling for reason-based routing:
if errs.HasAnyReason(err, "UNKNOWN_FORMAT", "WRITER_REQUIRED") {
    log.Warn("configuration-class error", "err", err)
}
```

</div>

<div role="tabpanel" id="errs-hasreason" aria-labelledby="errs-hasreason-btn" hidden>

**Best for:** matching by the screaming-snake reason rather than the numeric code — useful when humans write the test or the call site reads more naturally with a string.

```go
if errs.HasReason(err, "UNKNOWN_FORMAT") {
    log.Warn("unknown codec requested", "err", err)
}
```

</div>

<div role="tabpanel" id="errs-prefix" aria-labelledby="errs-prefix-btn" hidden>

**Best for:** "all errors from this package" / "all kernel errors" — CIDR-style routing on the dotted-quad code. Combine with `errors.Is`.

```go
// Match any code under the codec package (range 1.2.0.*).
codecRange := errs.NewPrefixMatcher(
    errs.Pack(1, 2, 0, 0),
    errs.MaskByPackage,
)
if errors.Is(err, codecRange) {
    metrics.IncCounter("codec_errors_total")
}
```

</div>

<div role="tabpanel" id="errs-render" aria-labelledby="errs-render-btn" hidden>

**Best for:** rendering an SDK error to a consumer-facing channel (HTTP body, gRPC status, CLI). `PublicOf` is wire-safe (≤120 runes, no newline); `PrivateOf` is diagnostic-only — never surface it.

```go
// Wire-safe message for the API caller.
w.WriteHeader(errs.HTTPStatusOf(err))
fmt.Fprintln(w, errs.PublicOf(err))

// Diagnostic — log only, never expose.
log.Error("internal failure",
    "code",    errs.CodeOf(err),
    "reason",  errs.ReasonOf(err),
    "private", errs.PrivateOf(err),
)
```

</div>

</div>
