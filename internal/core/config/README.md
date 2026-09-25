# config

Package `config` declares the SDK configuration port: `Source` (a config layer),
`Validator` (optional self-check on the decoded struct), `Watcher` (change
notification), `DeclaredValue` (one key and the typed value it takes when no
source supplied it), and the provenance pair — `OriginValue` (which layer
supplied a key's final value in a traced load, never the value) and
`Describer` (the sibling a `Source` implements to say where its values come
from). Concrete env/file sources, the merge+decode loader, the schema compiler,
the traced loads and the cross-OS poll watcher live in
`internal/service/config`; facade: `pkg/v1/config`. ADR 0028 + ADR 0061 +
ADR 0097. See `CLAUDE.md`.
