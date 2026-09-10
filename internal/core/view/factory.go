// Package view — the plug-in contract an engine implements to enter the
// registry.
package view

// Engine names a registered template engine.
//
// The zero value is the reserved invalid name: [Register] refuses it at boot,
// so it can never reach [Lookup], [Open] or [Available].
type Engine string

// HTML is the [Engine] name of the stdlib html/template implementation in
// internal/service/view. It is the only engine the SDK ships.
const HTML Engine = "html"

// Factory builds a [Renderer] of one engine from a [Config].
//
// Two methods, and the registry hands instances back to callers so each engine
// keeps its concrete type unexported — the stable contract is this interface.
//
// # What an engine undertakes by registering
//
// Registration is not merely a name binding; it is a promise the security model
// rests on. An engine in this registry MUST:
//
//   - Contextually escape every value it renders, for the media type its
//     [Renderer.ContentType] reports. An engine that does not escape may not
//     report an HTML content type.
//   - Render into its own buffer and return complete bytes or none.
//   - Honour [Config.MaxBytes], clamping a non-positive value to
//     [DefaultMaxBytes].
//   - Refuse at construction a [Config] it cannot honour, and refuse at render
//     time — by name, with [TemplateNotFound] — a template it does not hold.
//   - Name every template by its SLASH PATH within [Config.FS].
//
// That last one looks cosmetic and is not. html/template's own ParseFS names
// templates by filepath.Base, so a tree holding admin/page.html and
// user/page.html ends up with ONE template called "page.html" — measured, the
// second parse silently wins and every request for the admin page renders the
// user page. Full-path naming is the SDK refusing to inherit that.
type Factory interface {
	// Engine returns the registry key this factory claims. It must be stable
	// and non-empty.
	Engine() Engine

	// New builds a Renderer from cfg, parsing the whole template tree eagerly.
	//
	// Eager parsing is the point: a template that cannot be parsed, or cannot
	// be contextually escaped, will fail identically forever, so the failure
	// belongs in the process's first second — a deploy that does not come up —
	// rather than in the first request that happens to reach that page.
	New(cfg Config) (Renderer, error)
}
