# config (service)

Concrete env + file `Source`s, the merge+decode+validate `Load[T]`, the compiled
**schema** (`NewSchemaValue` + `LoadSchema`: required keys, typed defaults, a
closed key vocabulary — ADR 0061), and a cross-OS poll `Watcher` implementing
`internal/core/config`. File parsing dispatches through the codec registry.
Constraints come from `internal/service/validation`; this package owns no rule
vocabulary. Public facade: `pkg/v1/config`. ADR 0028 + ADR 0061.
See `CLAUDE.md` and `BENCH.md`.
