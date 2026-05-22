# pkg/v2/logger (preview)

Placeholder for the v2 logger surface. The real v1 logger lives at
`pkg/v1/logger/README.md`; this stub exists so the docs site can
render a `/v2/local/logger/` route while the v2 API is still being
designed.

## Planned changes (TBD)

- `slog.Handler` adapter as a first-class sink (interop with the
  Go stdlib).
- Default sink switches from text to JSON (CLI flag opts back).
- `Build` builder split into `RecordBuilder` and `AttrBuilder`
  to enforce single-responsibility on the call sites.

Subject to change as the v2 design lands.
