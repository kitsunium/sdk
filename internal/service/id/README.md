# id (service)

Concrete identifier generators (UUIDv4/v7, ULID, snowflake, NanoID, KSUID,
TypeID) implementing `internal/core/id.Generator`. All but TypeID register on
import; TypeID needs a caller-supplied type prefix, so it is built with
`NewTypeID`. KSUID and TypeID also decode — `ParseKSUID`, `ParseTypeID` and
`FormatTypeID`. Stdlib-only, cross-OS portable. Public facade: `pkg/v1/id`.
ADR 0024. See `CLAUDE.md`.
