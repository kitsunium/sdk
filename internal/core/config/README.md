# config

Package `config` declares the SDK configuration port: `Source` (a config layer),
`Validator` (optional self-check on the decoded struct), `Watcher` (change
notification), and `DeclaredValue` (one key and the typed value it takes when no
source supplied it). Concrete env/file sources, the merge+decode loader, the
schema compiler and the cross-OS poll watcher live in `internal/service/config`;
facade: `pkg/v1/config`. ADR 0028 + ADR 0061. See `CLAUDE.md`.
