package view

// Engine names a registered template engine.
//
// The zero value is the reserved invalid name: [Register] refuses it at boot,
// so it can never reach [Lookup], [Open] or [Available].
type Engine string

// HTML is the [Engine] name of the stdlib html/template implementation in
// internal/service/app/view. It is the only engine the SDK ships.
const HTML Engine = "html"
