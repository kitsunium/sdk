package plug

import "sync/atomic"

// kit's catalogues after English (ADR 0179 §3, step 8, Q6): what kit's core
// asks of the languages it speaks after English, which only the Studio
// shows. framework/app/studio sets the hook and
// framework/internal/app/studioengine implements it over the SDK's i18n. The
// core holds English itself — its own table, read from locales/en.json — and
// imports none of the SDK's i18n: the terminal, the logs and an error read
// English whatever a program imports, and a diagnostic's other languages are
// rendered through the hook, opened once with the English table. The value is
// here, the catalogues in words_interface.go.

// Words is the framework's sentences in the languages after English, the catalogues
// the Studio shows its reader's language from; nil until
// framework/app/studio is imported, and the framework then speaks English alone.
// The terminal, the logs and an error read English whatever a program
// imports (ADR 0179 Q6).
var Words atomic.Pointer[WordsConfig]

// WordsConfig is what framework/app/studio plugs into kit: how the framework's
// catalogues in the languages after English open.
type WordsConfig struct {
	// Open reads and compiles the catalogue of every language after
	// English, each falling back to english — the framework's own sentences, by
	// key, as its core holds them; a catalogue that does not decode or
	// does not compile is refused.
	Open func(english map[string]string) (Translator, error)
}
