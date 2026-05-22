# pkg/v2/codec (preview)

Placeholder for the v2 codec surface. The real v1 codec lives at
`pkg/v1/codec/README.md`; this stub exists so the docs site can
render a `/v2/local/codec/` route while the v2 API is still being
designed.

## Planned changes (TBD)

- Streaming-first surface (no batch `Marshal`/`Unmarshal` shortcut).
- Format-name typed as a Go enum, not a string.
- Drop the base-N family in favour of `pkg/v2/baseenc` (separate
  package — pure byte transform, not a codec dispatch target).

None of the above is final. See ADR 0007 for how `pkg/v2/` graduates.
