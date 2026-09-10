// Package view — range 0.2.27.* (ADR 0058 core/view block).
package view

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.2.27.0 - 0.2.27.255

// CodeViewMisconfigured identifies a Renderer refused AT CONSTRUCTION because
// its [Config] cannot be honoured — most importantly a nil FS.
//
// Every permanent fault a template tree can carry is discovered here, on
// purpose: a tree that does not parse, or a template html/template cannot
// contextually escape, will fail identically on every request forever, and a
// deploy that refuses to come up is a cheaper way to learn that than a 500 on
// the one page nobody smoke-tests.
const CodeViewMisconfigured errs.Code = 0x00_02_1B_01 // 0.2.27.1

// CodeTemplateNotFound identifies a Render for a name the engine does not hold.
//
// It is a normal, expected verdict rather than a broken renderer: a Renderer
// built over an empty tree is legitimate and refuses everything by name
// (ADR 0031). The name asked for is a Field, never the Public — a template name
// is part of the application's routing surface.
const CodeTemplateNotFound errs.Code = 0x00_02_1B_02 // 0.2.27.2

// CodeRenderFailed identifies a template that started executing and stopped —
// a nil pointer in a field chain, a method that returned an error, a range over
// the wrong type.
//
// The engine's diagnostic for this is the single most leak-prone string in the
// domain. html/template renders it as, verbatim,
// `template: /srv/app/web/admin/page.html:1:7: executing "…" at <.User.Name>:
// nil pointer evaluating interface {}.Name` — an absolute filesystem path, a
// line and column, a fragment of the template's own source, and a Go type name.
// All of it is reconnaissance, all of it is Private.
const CodeRenderFailed errs.Code = 0x00_02_1B_03 // 0.2.27.3

// CodeRenderTooLarge identifies a render stopped at [Config.MaxBytes].
//
// It is a denial-of-service guard, not a formatting complaint. A {{range}} over
// a collection an attacker can grow has no recursion depth, so the stdlib's own
// 100000-frame limit never fires; the byte cap is the only thing between that
// template and the machine's memory. The partial output is discarded, never
// returned.
const CodeRenderTooLarge errs.Code = 0x00_02_1B_04 // 0.2.27.4

// CodeUnsafeValue identifies render data carrying one of the six html/template
// trust types the domain refuses: CSS, HTMLAttr, JS, JSStr, URL or Srcset.
//
// Each of them disables contextual escaping for the value it wraps, and each
// has a working exploit that plain strings do not: measured,
// template.URL("javascript:alert(1)") reaches the browser as
// href="javascript:alert%281%29" and executes, while the same string
// unconverted is rewritten to the inert "#ZgotmplZ".
const CodeUnsafeValue errs.Code = 0x00_02_1B_05 // 0.2.27.5

// CodeEngineUnknown identifies an [Open] for an [Engine] no factory has
// registered — usually a name resolved from configuration with a typo in it,
// or an engine whose package was never imported.
const CodeEngineUnknown errs.Code = 0x00_02_1B_06 // 0.2.27.6

// CodeEngineInvalid identifies a boot-time [Register] of a nil Factory or one
// claiming the empty [Engine] name. It panics: a broken registration is a
// programming error that must be visible at import, not a value to thread
// through a call chain nobody is executing yet.
const CodeEngineInvalid errs.Code = 0x00_02_1B_07 // 0.2.27.7

// CodeDuplicateEngine identifies two distinct factories claiming one [Engine]
// name. It panics for the same boot-time reason as [CodeEngineInvalid], and it
// matters more here than in a codec registry: silently letting the second
// registration win could swap the engine that escapes for one that does not.
const CodeDuplicateEngine errs.Code = 0x00_02_1B_08 // 0.2.27.8
