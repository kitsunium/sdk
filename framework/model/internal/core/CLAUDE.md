# framework/model/internal/core — the Product Graph's code

## Purpose

Everything `framework/model` exposes, under the names KTN-STRUCT-ROLE asks
for: each struct carries its role (`NodeEntity`, `GraphMessage`,
`EndpointSpec`, `IDValue`, …). `framework/model` aliases each one under the
graph's own name and is the only importer (Bazel visibility
`//framework/model:__subpackages__`).

The map of names, the contents file by file, the sentinels and the rules are
in `../../CLAUDE.md`: the two packages are one API.

## Rules

- A new struct gets a role suffix here and an alias in the public file of the
  same name. Its doc starts with the internal name; the public doc is the
  same text under the public name.
- A struct another embeds keeps the public name as its field name through an
  alias here (`MailSummary = MailSummaryMessage`): renaming an embedded type
  renames the field.
- Big structs (`NodeEntity`, `EdgeMessage`, `ConnectorSpec`) go by pointer;
  a function that strips or changes one copies it first (`structuralNode`,
  `mergeLoop`, `mergeTransitions`, `mergeAuthorize`): a merged graph shares
  its pointers with its base.

## Do NOT

- Import `framework/model` here (cycle), or anything of the runtime.
- Export a name that has no alias in `framework/model`.
