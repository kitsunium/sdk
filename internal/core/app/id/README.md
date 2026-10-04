# id

Package `id` declares the SDK's identifier-generation port: the `Generator`
contract and the typed `Scheme` registry. Concrete schemes (UUIDv4/v7, ULID,
snowflake, NanoID, KSUID) live in `internal/service/app/id` and self-register on
import; TypeID lives there too but needs a caller-supplied type prefix, so it is
built with `NewTypeID` rather than registered. The public facade is `pkg/v1/app/id`.

```go
uid, err := id.New("uuidv7")   // dispatch by scheme
```

See `CLAUDE.md` for the maintainer contract. ADR 0024.

The `Generator` port is generated from `design/app/id.yaml` into
`design_gen.go` (ADR 0163): it changes in the design, then `kit gen`.
