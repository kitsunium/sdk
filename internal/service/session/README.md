# session

Package `session` provides the concrete half of the SDK session domain: an
in-memory store, an on-disk store, and the AEAD sealer that renders a session
identifier as a cookie value. All three implement the ports in
`internal/core/session`.

Both stores read time through `internal/kernel/clock.Clock` — the narrow half,
because a store reads time and never waits on it — so every expiry assertion in
the suite advances a `ManualClock` instead of sleeping.

The file store seals every record with AES-256-GCM, binds each record to its own
filename, names files by `ID.Digest` so no identifier is ever on disk, narrows
and then *asserts* owner-only permissions, publishes through a temporary file and
`rename(2)`, and serialises every read-modify-write under one exclusive `flock`.
Where those mechanics do not exist — Windows, wasip1, any GOOS without
`flock(2)` — it returns `proc.UnsupportedPlatform` at construction rather than
pretending.

Rotating a session keeps its absolute ceiling when the subject is unchanged, and
restarts both clocks when the subject changes.

Ports: `internal/core/session`; facade: `pkg/v1/session`. ADR 0045.
See `CLAUDE.md`.
