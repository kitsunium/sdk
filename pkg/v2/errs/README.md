# pkg/v2/errs (preview)

Placeholder for the v2 errors surface. The real v1 errors live at
`pkg/v1/errs/README.md`; this stub exists so the docs site can
render a `/v2/local/errs/` route while the v2 API is still being
designed.

## Planned changes (TBD)

- Carry a request/trace ID slot through the `*errs.Error` envelope
  so handlers don't have to wrap manually.
- Drop the `MaskBy*` typed constants in favour of a single
  `code.Mask(level)` accessor.
- Public `Format()` mirrors the dotted-quad rendering of `Error()`
  for log formatters that bypass `%v`.

Subject to change as the v2 design lands.
