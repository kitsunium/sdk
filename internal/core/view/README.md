# view (core)

The SDK's server-side rendering **domain** (ADR 0058): the `Renderer` port, the
`Factory` registry that resolves one engine by name, and the single trust type
through which a caller can deliberately bypass escaping. The one engine that
ships lives in `internal/service/view`.

```go
type Renderer interface {
	Render(ctx context.Context, name string, data any) ([]byte, error)
	ContentType() string
}
```

`Render` returns bytes and never takes an `io.Writer`: template execution can
fail halfway, and handed an `http.ResponseWriter` it would already have flushed
the status line, the headers and a plausible prefix of the page before the
failure is detectable — at which point 500 is no longer sendable.

`TrustedHTML` / `TrustHTML` are the ONE bypass and the only spelling the SDK
offers. The other six `html/template` trust types — `CSS`, `HTMLAttr`, `JS`,
`JSStr`, `URL`, `Srcset` — are refused in render data and have no spelling
here at all. Read `CLAUDE.md` §Why this shape and ADR 0058 §D4 for what that
buys and, more importantly, what it does not: a `Trusted` the caller built
wrongly is an XSS the SDK cannot see.

Error range `0.2.27.*`. Maintainer rationale lives in `CLAUDE.md`.
