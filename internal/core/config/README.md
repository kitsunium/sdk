# config

Package `config` declares the SDK configuration port: `Source` (a config layer),
`Validator` (optional self-check on the decoded struct), and `Watcher` (change
notification). Concrete env/file sources, the merge+decode loader, and the
cross-OS poll watcher live in `internal/service/config`; facade: `pkg/v1/config`.
ADR 0028. See `CLAUDE.md`.
