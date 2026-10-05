package redact

import (
	"encoding/json"
)

// Placeholder replaces every secret a [Redactor] recognises. A caller that
// shows a redacted value compares against it, so every implementation writes
// exactly this text.
const Placeholder string = "[redacted]"

// Ellipsis ends a string, a text or a document cut to fit its bound. It is one
// character, three bytes of UTF-8, and it is counted inside the bound.
const Ellipsis string = "…"

// MinBytes is the smallest bound a [Redactor] honours; a smaller or
// non-positive one is raised to it. Below it a cut document could not hold
// its own marker and the closers of the containers it cut — and a bound is the
// display's size, so raising a nonsensical one is safer than refusing to show
// anything (ADR 0031's clamp: the floor is obvious).
const MinBytes int = 16

// Unencodable is the text a log attribute holding a value encoding/json
// refuses is rendered as by [Redactor.Attrs]: the marker, never the value.
const Unencodable string = "[unencodable]"

// DocumentValue is a JSON document with its secrets replaced, and whether it
// had to be cut to fit its bound. It is what [Redactor.JSON] and
// [Redactor.Value] return.
type DocumentValue struct {
	// JSON is always one well-formed JSON value, never longer than the bound.
	JSON json.RawMessage
	// Truncated reports that members, elements or the tail of a string were
	// left out to fit the bound. The containers that were cut are still
	// closed, so JSON stays well-formed.
	Truncated bool
}
