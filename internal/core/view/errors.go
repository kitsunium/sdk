// Package view — declares the sentinel *errs.Error port outcomes. Each var's
// name equals its errs.Define Reason in SCREAMING_SNAKE form.
//
// No Public string in this package names a template, a path, a line, a data key
// or a fragment of template source. A Public is written to third parties, and
// everything a template engine handles is either the application's internal
// structure or somebody's data. What an operator needs travels in Fields and in
// Private; what a browser gets is one sentence with nothing in it.
package view

import "github.com/kitsunium/sdk/internal/kernel/errs"

// exitConfig matches sysexits EX_CONFIG (78). A Renderer refused at
// construction is a permanent fault: the same tree will be refused forever, and
// the fix is an edit, never a retry.
const exitConfig int = 78

// exitSoftware matches sysexits EX_SOFTWARE (70) — the errs default, restated
// where a sentinel wants it explicitly.
const exitSoftware int = 70

// httpInternal is 500. Every render verdict in this domain is a server fault:
// the caller asked for a page, and which page failed is not something the
// browser gets to learn.
const httpInternal int = 500

var (
	// ViewMisconfigured is returned by a [Factory]'s New for a [Config] it
	// cannot honour: a nil FS, a template tree that does not parse, or a
	// template html/template cannot contextually escape.
	//
	// The escaping half is the one worth naming. html/template resolves a
	// template's escaping context lazily, at its FIRST execution, and reports
	// three classes of failure there — a template that ends inside an
	// unterminated tag, an {{if}} whose branches end in different contexts, and
	// a {{template}} naming something that does not exist. Left alone, all
	// three are 500s on a live request. The engine therefore forces escaping at
	// construction and turns them into this.
	ViewMisconfigured = errs.Define(CodeViewMisconfigured, "VIEW_MISCONFIGURED",
		"The view renderer cannot be built from the given configuration",
		"core/view: a Factory received a nil FS, a template that does not parse, or one html/template cannot contextually escape; the fields name the template and the private message carries the engine's own diagnostic",
		errs.WithExitCode(exitConfig))

	// TemplateNotFound is returned by Render for a name the engine does not
	// hold, including the empty name.
	//
	// It is deliberately not a construction-time concern: a Renderer over an
	// empty tree is legitimate and refuses everything by name. That is ADR
	// 0031's shape — the configuration nobody filled in produces a loud,
	// typed refusal on every call rather than an inert renderer that returns
	// empty pages.
	TemplateNotFound = errs.Define(CodeTemplateNotFound, "TEMPLATE_NOT_FOUND",
		"The requested view does not exist",
		"core/view: Render was called with a name absent from the parsed set; the fields carry the name asked for and the number of templates the renderer holds",
		errs.WithHTTPStatus(httpInternal))

	// RenderFailed is returned when execution started and could not finish.
	//
	// Nothing is returned alongside it. The bytes produced before the failure
	// are discarded inside the engine, which is the whole reason Render returns
	// a slice instead of taking a writer: handed an http.ResponseWriter, the
	// same failure would have flushed the status line, the headers and a
	// plausible-looking prefix of the page before it was detectable — measured
	// at 17 bytes for a template that fails on its second action.
	RenderFailed = errs.Define(CodeRenderFailed, "RENDER_FAILED",
		"The page could not be rendered",
		"core/view: template execution failed after it began; the private message carries html/template's own diagnostic, which names the file path, the line, the column and a fragment of template source and must never be shown to a caller",
		errs.WithExitCode(exitSoftware), errs.WithHTTPStatus(httpInternal))

	// RenderTooLarge is returned when a render reached [Config.MaxBytes].
	//
	// The engine stops at the ceiling rather than after it: the limit is
	// enforced by the writer html/template is executing into, so the bytes are
	// never accumulated in the first place and the process does not have to
	// survive the allocation in order to reject it.
	RenderTooLarge = errs.Define(CodeRenderTooLarge, "RENDER_TOO_LARGE",
		"The page could not be rendered",
		"core/view: rendering reached Config.MaxBytes and was stopped at the ceiling; the fields carry the limit and the template name",
		errs.WithHTTPStatus(httpInternal))

	// UnsafeValue is returned when render data carries one of the six refused
	// html/template trust types.
	//
	// It is a programming error surfaced as a runtime verdict, and it is
	// refused BEFORE the template runs so no partial page exists to reason
	// about. The field naming the location is a path — data.Items[3].Body,
	// built from GO field names because that is the vocabulary a template
	// author reads and a developer greps — because "an unsafe value is
	// somewhere in your model" is not something a developer can act on at
	// three in the morning.
	UnsafeValue = errs.Define(CodeUnsafeValue, "UNSAFE_VALUE",
		"The page could not be rendered",
		"core/view: render data carries an html/template trust type that disables contextual escaping (CSS, HTMLAttr, JS, JSStr, URL or Srcset); the fields name the path and the type, and view.TrustHTML is the only trusted spelling the SDK offers",
		errs.WithExitCode(exitSoftware), errs.WithHTTPStatus(httpInternal))

	// EngineUnknown is returned by [Open] for a name no factory claims.
	//
	// It carries the registered names in a Field so the usual cause — an engine
	// package that was never imported, so its registration never ran — is
	// answerable from one log line.
	EngineUnknown = errs.Define(CodeEngineUnknown, "ENGINE_UNKNOWN",
		"No view engine is registered under that name",
		"core/view: Open received an Engine no Factory has registered; the fields carry the requested name and the registered ones, and the usual cause is an unimported engine package",
		errs.WithExitCode(exitConfig))

	// EngineInvalid is the boot-time panic for a nil Factory or an empty
	// [Engine] name.
	EngineInvalid = errs.Define(CodeEngineInvalid, "ENGINE_INVALID",
		"The view engine registration is not usable",
		"core/view: Register received a nil Factory or one claiming the empty Engine name; the empty name is what an uninitialised variable holds and is refused at boot so it can never reach Lookup",
		errs.WithExitCode(exitConfig))

	// DuplicateEngine is the boot-time panic for two distinct factories under
	// one [Engine] name.
	//
	// Re-registering the SAME factory is idempotent and silent; two different
	// ones are a hard conflict, because last-write-wins in THIS registry can
	// mean the engine that escapes was replaced by one that does not, at import
	// time, with no call site to blame.
	DuplicateEngine = errs.Define(CodeDuplicateEngine, "DUPLICATE_ENGINE",
		"Two view engines claim the same name",
		"core/view: a second, distinct Factory registered under an Engine name already taken; refused at boot rather than resolved, since one of the two would silently decide how every value in the program is escaped",
		errs.WithExitCode(exitConfig))
)
