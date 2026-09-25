# redact (internal/service/redact)

Renders values, JSON documents, text and log attributes for DISPLAY with their
secrets replaced, within an exact byte bound, never mutating the input.
Internal service implementation behind the public `pkg/v1/redact` facade —
consumers import the facade, not this package.

## API

```go
func NewRedactor(cfg Config) *Redactor
func DefaultWords() []string

func (r *Redactor) Name(name string) bool
func (r *Redactor) Text(s string, maxBytes int) string
func (r *Redactor) JSON(document []byte, maxBytes int) (DocumentValue, error)
func (r *Redactor) Value(v any, maxBytes int) (DocumentValue, error)
func (r *Redactor) Attrs(attrs []corelogger.AttrValue, maxBytes int) iter.Seq2[string, string]
```

A secret is a NAME containing one of the configured words, a struct field
DECLARED secret by the configured tag or field rule, or the CREDENTIALS of a
URL inside a string.

## Errors

| Code | Reason | When |
|---|---|---|
| `0.3.73.1` | `DOCUMENT_INVALID` | `JSON` got something that is not exactly one JSON value |
| `0.3.73.2` | `VALUE_UNENCODABLE` | `Value` got something `encoding/json` refuses |

Neither refusal repeats a byte of what it refused.
