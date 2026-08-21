# id (service)

Concrete identifier generators (UUIDv4/v7, ULID, snowflake) implementing
`internal/core/id.Generator`, registered on import. Stdlib-only, cross-OS
portable. Public facade: `pkg/v1/id`. ADR 0024. See `CLAUDE.md`.
