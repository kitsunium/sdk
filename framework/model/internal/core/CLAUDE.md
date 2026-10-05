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

## Declarations

`decl_gen.go` is written by kit gen from the design (ADR 0170): the declarations of `ArchitectureMessage`, `PersonMessage`, `SystemMessage`, `ContainerMessage`, `PortMessage`, `LinkMessage`, `CodeResult`, `CodeFuncMessage`, `CodeStepMessage`, `CodeBlockMessage`, `CodeEffectMessage`, `CodeUseMessage`, `ConnectorMessage`, `ConnectorKind`, `ConnectorSpec`, `AdapterMessage`, `MailMessage`, `EdgeKind`, `EventType`, `StoreHistoryMessage`, `PasswordPolicyMessage`, `FormerMessage`, `RecordHistoryMessage`, `RecordVersionMessage`, `EditMessage`, `RecordVersionsMessage`, `IDValue`, `GraphMessage`, `AppMessage`, `BuildMessage`, `ModuleVersionMessage`, `SourceMessage`, `EdgeMessage`, `StatsMessage`, `DiagnosticMessage`, `AnalysisResult`, `ModuleMessage`, `FileMessage`, `NodeEntity`, `EndpointSpec`, `PortSpec`, `CommandSpec`, `QuerySpec`, `PermissionMessage`, `StoreSpec`, `StorePrivacyMessage`, `ProductRetentionSpec`, `RetentionSpec`, `HeldUntilSpec`, `IndexSpec`, `TopicSpec`, `SubscriptionSpec`, `WorkflowSpec`, `StateSpec`, `TransitionSpec`, `JobSpec`, `FrontendSpec`, `AuthSpec`, `MailerSpec`, `SecretSpec`, `LoopSpec`, `WakeSourceMessage`, `SelectCaseMessage`, `MechanicMessage`, `SchemaMessage`, `FieldMessage`, `NodeKind`, `BinarySpec`, `RoleSpec`, `CLISpec`, `ListenerSpec`, `LibrarySpec`, `PresentationSpec`, `PersonalDataMessage`, `StoreDataMessage`, `ExportedVersionsMessage`, `ErasureMessage`, `StoreErasureMessage`, `PrivacyMessage`, `RegisterMessage`, `RegisterStoreMessage`, `HoldEvent`, `JournalEntryEvent`, `JournalCheckMessage`, `RecordFormerMessage`, `ProfileResult`, `NodeCostMessage`, `FuncCostMessage`, `FlameNodeMessage`, `GoroutinesResult`, `GoroutineGroup`, `RuntimeMessage`, `DatabaseMessage`, `Pool`, `MigrationSetMessage`, `MigrationMessage`, `SettingMessage`, `BootStepMessage`, `DevBuildMessage`, `PhaseChangeEvent`, `ProcessSpec`, `HTTPServer`, `ComponentMessage`, `LoopMessage`, `Event`, `SpanMessage`, `Payload`, `TransitionEvent`, `CensusMessage`, `TraceMessage`, `SnippetMessage`, `InstanceMessage`, `StepEvent`, `LogRecordMessage`, `MockMessage` and `MailSummaryMessage` — each struct with every field, unexported ones included. Their methods, constructors and helpers stay hand-written, in the files this document names.
