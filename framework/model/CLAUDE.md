<!-- updated: 2026-10-03T13:06:04Z -->
# framework/model — the Product Graph

## Purpose

The JSON contract every part of the framework shares, and the grammar of
every ID it carries — written once (ADR 0147 §4). Moved from
`kitsunium/platform/model` at `Version` 5; the platform's `model` becomes a
facade of aliases over this package, and its `studio/web/src/model.ts` is
generated from the patterns and types here.

Stdlib plus `internal/kernel/errs` (for `INVALID_ID`): a design-file
generator, an analyzer and a Studio import it without importing the runtime.

## Layout

Two packages, one API:

- `framework/model` — the public face and the one import path. Each file
  mirrors the `internal/core` file of the same name: an alias per type under
  the graph's own name (`Node`, `Graph`, `EndpointInfo`, …), its constants
  (typed), its variables and a
  wrapper per function. Methods come with the aliases.
- `internal/core` — the code, its structs named by their role as
  KTN-STRUCT-ROLE asks (`NodeEntity`, `GraphMessage`, `EndpointSpec`,
  `CodeResult`, `IDValue`, …). Only `framework/model` imports it.

| Public | `internal/core` |
|---|---|
| `Node` | `NodeEntity` |
| `ID` | `IDValue` (pointer receivers: `(*ID).String`) |
| every `*Info` but `CodeInfo` | `*Spec` (`EndpointInfo` → `EndpointSpec`, `BinaryInfo` → `BinarySpec`, …) |
| `CodeInfo` | `CodeResult` |
| `Analysis`, `Profile`, `Goroutines` | `…Result` |
| `Retention`, `HeldUntil`, `Process` | `…Spec` |
| `Hold`, `JournalEntry`, `PhaseChange`, `Step` | `…Event` |
| every other struct (`Graph`, `Edge`, `App`, `Source`, …) | `…Message` |
| `GoroutineGroup`, `Pool`, `HTTPServer`, `Payload` | the same name, no role suffix |
| `MailSummary` | `MailSummaryMessage`, and the alias `MailSummary` that `MailMessage` embeds so its field keeps its name |

## Contents (`internal/core`; the public file of the same name aliases it)

| File | Holds |
|---|---|
| `model.go` | `Graph`, `App` (its `Build`), `Source`, `Edge` (its `Contract`), `Stats`, `Diagnostic`, `Analysis`, `Version` (5: binaries, roles, CLI commands, listeners, libraries, presentations, the `contracts`/`runs`/`imports`/`renders` edges; the Studio's mock modes and control event removed — D13) |
| `node.go` | `Node` — in `internal/core` its declaration is kit's, in `decl_gen.go`, with every other type this table names that is declared alone (ADR 0170); the public `node.go` aliases it |
| `node_kind.go`, `edge_kind.go`, `event_type.go` | the closed sets `NodeKind`, `EdgeKind`, `EventType`: strings, each marked `//ktn:wire-format` |
| `id.go` | the ID grammar: `SegmentPattern`, `NamePattern`, `RoutePattern`, `ContractPattern`, `IDPattern` (one anchored expression, the one a JSON Schema publishes), `ID` + `ParseID` + `String`, `ValidSegment`/`ValidName`/`ValidContract`, `BinaryID`/`RoleID`/`LibraryID` |
| `id_grammar.go` | the patterns' words matched by hand (`wordOf`, `validRoute`), never compiled: RE2 expands `{0,62}` into as many states, and the four compiled patterns cost 0.6 ms and 2 500 allocations at every start of every product, which declares its names before `main`; `TestTheMatchersAgreeWithThePatterns` holds them to the patterns |
| `node_info.go` | kind-specific details (`SecretInfo`, `PortInfo`, `EndpointInfo`, `StoreInfo`, `CommandInfo`, `QueryInfo`, …), `Mechanic`, `Schema`, `Field` |
| `node_process.go` | what a product is made of beyond its services: the `Profile*` constants (`server`, `cli`, `daemon`), the `Network*` constants, `BinaryInfo`, `RoleInfo`, `CLIInfo`, `ListenerInfo`, `LibraryInfo`, `PresentationInfo` |
| `code.go` | a node's code level, C4's fourth (`CodeInfo`, `CodeFunc`, `CodeStep`s under `CodeBlock`s) |
| `devtools.go` | the mail statuses, `MailMessage`, `MockReplace` |
| `profile.go` | a CPU or heap `Profile` folded onto the nodes, `Goroutines` grouped |
| `doc.go` | `SplitDoc`: a doc comment's default text and its translations (`docTag` compiled at its first use) |
| `history.go` | what a store remembers: `StoreHistory`, `Former`, `RecordHistory` |
| `privacy.go` | `PersonalData`, `RecordFormer`, `Erasure`, the Privacy page, retention modes, the journal's operations |
| `runtime.go` | `Runtime`, `Loop`, `Component`, live `Event`s (`LogRecord`, `MailSummary`), `Mock`, `Span` (its operations, `connect` and `cli` among them), `Trace`, `Database`, `Setting` |
| `connector.go`, `architecture.go` | the connector table, the catalog's `Adapter`s (imports under `framework/connectors/`), the C4 `Container`s |
| `graph.go` | `NodeID`, `EdgeID`, `Normalize`, `Revision`, `Merge`, `Files` |
| `module.go` | `Module`, `QualifiedService`, `UnderPrefix`, `File` |
| `mermaid.go` | the architecture as a Mermaid flowchart |
| `table.go` | `TableName(service, node, suffix)` — the table a database keeps a kit table in: `<service>__<node><suffix>`, lower case, `-` and `.` written `_`; a name the rule cannot keep as it is (upper case, past `MaxTableLen`, three underscores in a row, SQLite's `sqlite_` prefix) is cut and ends with a digest of the whole — and `MaxTableLen` (58: the SQL document store's `___ix` index table must fit PostgreSQL's 63). The runtime and the analyzer both name tables with it |
| `errors.go` | `0.4.1.*`: the codes and their sentinels |

## Sentinels

| Code | Reason | When |
|---|---|---|
| `0.4.1.1` | `INVALID_ID` | `ParseID` on a string `IDPattern` does not match; field `part` names what failed, never the input |

## Rules

- **One grammar.** `IDPattern` and `ParseID` accept exactly the same
  strings — `TestParseIDRefusesWithoutQuotingTheInput` checks both on every
  case. A new kind a service holds joins `serviceKinds` and therefore both.
- `Revision` hashes structure only — counters, counts, dead letters, a
  store's privacy counters and history weight are stripped first
  (`structuralNode`).
- `Normalize` gives a graph without edges an empty list, never null.
- `Merge` trusts the base (runtime) for which nodes exist, which modules it
  mounts, what each port calls and which stores feed each watch.
- `Files` is the source endpoint's allow-list: every `Source` a graph can
  carry must be added there.
- Bump `Version` for any change an older reader would misread.
- `NodeKind`, `EdgeKind` and `EventType` stay strings under
  `//ktn:wire-format`: not an exclusion, the declaration KTN-CONST-STRENUM
  provides for an enumeration that is a wire format — the graph's JSON and
  the Studio's TypeScript unions.
- **The public names do not move.** A struct added to `internal/core` gets
  its role suffix there and its alias here, in the file of the same name;
  a constant is redeclared here typed, `= core.<Name>`.

## Do NOT

- Add a kind without deciding where its ID lives: under a service
  (`serviceKinds`), or under a scope prefix (`binary:`, `library:`).
- Quote the input in a refusal — `ParseID` is handed strings from design
  files, URLs and CLIs.
- Import anything from the runtime (`framework/kit`) here: this package is
  what a tool imports without linking a framework.
