# redact

Package `redact` declares the SDK's display-redaction port: the `Redactor` that
renders a Go value, a JSON document, a text or log attributes for DISPLAY with
every secret it recognises replaced, within a byte bound, never touching what
it is given.

```go
type Redactor interface {
    Name(name string) bool
    Text(s string, maxBytes int) string
    JSON(document []byte, maxBytes int) (DocumentValue, error)
    Value(v any, maxBytes int) (DocumentValue, error)
    Attrs(attrs []corelogger.AttrValue, maxBytes int) iter.Seq2[string, string]
}
```

It also holds the values every implementation writes and every caller compares
against — `Placeholder` (`[redacted]`), `Ellipsis` (`…`), `MinBytes` (the floor
every bound is raised to), `Unencodable` and `DocumentValue` — and the domain's
two codes, `DOCUMENT_INVALID` (`0.3.73.1`) and `VALUE_UNENCODABLE`
(`0.3.73.2`).

It is a display filter, not an access control: what no rule recognises is
shown. The engine and its configuration live in
`internal/service/security/redact`; facade: `pkg/v1/security/redact`.
ADR 0101, ADR 0160. See `CLAUDE.md`.

The `Redactor` port is generated from `design/security/redact.yaml` into
`design_gen.go` (ADR 0163): it changes in the design, then `kit gen`.
