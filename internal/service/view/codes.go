// Package view — range 0.3.57.* (ADR 0058 service/view block).
package view

import "github.com/kitsunium/sdk/internal/kernel/errs"

// range: 0.3.57.0 - 0.3.57.255

// CodeTemplateSourceFailed identifies a template tree that could not be READ:
// a walk that failed, a file that could not be opened.
//
// It is kept distinct from a parse failure because the two have different
// operators and different fixes. A source failure is usually a packaging bug —
// an embed pattern that matched nothing, an os.DirFS pointed at a path that
// does not exist in the container — and it is answered by looking at the
// build, not at the template.
const CodeTemplateSourceFailed errs.Code = 0x00_03_39_01 // 0.3.57.1

// CodeTemplateParseFailed identifies a template file html/template refused to
// parse, or one it could not resolve a contextual escaping plan for.
//
// Both are construction-time on purpose. html/template resolves escaping
// lazily, at a template's first execution, so an {{if}} whose branches end in
// different contexts or a {{template}} naming something absent would otherwise
// surface as a 500 on whichever request first reached that page.
const CodeTemplateParseFailed errs.Code = 0x00_03_39_02 // 0.3.57.2
