# config (service)

Concrete env + file `Source`s, the merge+decode+validate `Load[T]`, and a
cross-OS poll `Watcher` implementing `internal/core/config`. File parsing
dispatches through the codec registry. Public facade: `pkg/v1/config`. ADR 0028.
See `CLAUDE.md`.
