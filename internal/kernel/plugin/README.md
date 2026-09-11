# plugin

The question a process-wide registry asks before it publishes anything — a
kernel primitive (stdlib-only, no domain vocabulary).

```go
func Register(c Codec) Codec {
    if why := plugin.Unusable(c); why != "" {
        panic(fmt.Sprintf("codec.Register [%s CODEC_NIL]: %s", CodeCodecNil, why))
    }
    …
}
```

`Unusable` returns the empty string for a value a registry can store, and a
reason fragment for the two shapes that satisfy an interface at compile time
and cannot serve one call:

- a **typed nil** — `(*gzipCompressor)(nil)` is not `== nil`, so the usual
  guard lets it through and the failure surfaces at the first dispatch, far
  from the import that caused it;
- a value whose type is **not comparable** — registries compare entries to tell
  an idempotent re-registration from a conflict, and `==` on such a value
  panics with Go's `comparing uncomparable type` instead of the registry's own
  duplicate error.

The reason is a fragment meant to follow a colon in the caller's message, so
each registry keeps its own dotted-quad code and reason.

ADR 0071. See `CLAUDE.md` for why it returns a string rather than an error.
