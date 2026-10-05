// Package lifecycle — ranges 0.2.19.* (ADR 0050 core/app/lifecycle block) and
// 0.3.49.* (ADR 0050 service/app/lifecycle block, declared here since ADR 0160).
//
// Package lifecycle — declares the sentinel *errs.Error outcomes of the
// domain: the registration refusals, and the engine's and the supervisor's
// verdicts on a run. Each var's name equals its errs.Define Reason in
// SCREAMING_SNAKE form.
//
// Package lifecycle declares the ordered start/stop port of the SDK: the
// [Start] that brings a component up, the [Stop] that takes it down, and the
// [Lifecycle] that owns an ordered set of them and drives it in both
// directions. A core sibling admitted by ADR 0050.
//
// Both halves of a component are FUNCTION ports rather than interfaces — the
// shape internal/core/CLAUDE.md already admits for resilience.Operation and
// scheduler.Job. Each is a single behaviour, so a named func IS the contract
// and needs no adapter at the call site; it is also the narrowest thing
// pkg/v1 can publish. ADR 0039's lesson is that a published port cannot grow
// a method without breaking every downstream implementer at compile time, and
// a func type cannot grow one at all.
//
// # What the order means
//
// The order components are added in IS the dependency order, and shutdown is
// its exact reverse. This package declares no graph, no edges and no
// DependsOn field: a linear sequence already IS a topological order, and the
// caller — who wrote the constructors — is the only party that knows it. The
// price is stated rather than hidden: the SDK cannot detect that the caller
// ordered them wrong, because it has nothing to check the order against.
//
// # What a component owns
//
// A [ComponentValue] is a pairing of a name, a Start and a Stop. Stop is
// called ONLY for a component whose Start returned nil, so a Stop may assume
// its Start succeeded — which is what makes a double-close impossible on the
// failure path. A Start that fails owns whatever it acquired before failing,
// exactly as a Go constructor does.
//
// The engine that drives the sequence, the shutdown budget, and the opt-in
// signal / sd_notify wiring live in internal/service/app/lifecycle; this package
// owns only the contract, the three domain values, and the typed sentinels a
// registration refuses with.
//
// Package lifecycle — hosts ComponentValue, the registration a Lifecycle owns.
//
// Package lifecycle — hosts Phase, the direction a transition moved in.
package lifecycle
