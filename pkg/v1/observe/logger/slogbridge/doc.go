// Package slogbridge — converts slog levels and attributes to their SDK peers.
//
// Package slogbridge — the slog.Handler that forwards records to an SDK Logger.
//
// Package slogbridge — range 1.1.1.* (ADR 0005 pkg/v1/observe/logger/slogbridge block).
//
// Package slogbridge adapts an SDK [logger.Logger] to log/slog so a foreign
// API that accepts only a *slog.Logger emits through the SDK pipeline.
//
// # Why this package exists
//
// The SDK logger deliberately does not depend on log/slog: internal/core is
// stdlib-only and its level sub-package states the rule outright ("specifically
// never log/slog"). That keeps the domain free of a foreign vocabulary — but it
// leaves consumers stranded whenever a library types its logging knob as the
// *concrete* *slog.Logger rather than an interface. The MCP Go SDK's
// mcp.ServerOptions.Logger is the canonical example.
//
// Without a bridge the only way out is a SECOND logger writing to the same
// stream, and that second pipeline is where the damage lives: two level
// thresholds parsed by two different rules, two line formats on one file, and
// "framework_version" stamped on only half the records. This package removes
// the second pipeline. slog becomes a façade over the SDK Logger — one
// threshold, one encoder, one set of sinks, one version stamp.
//
// slog is confined to this package on purpose. It is an adapter to a foreign
// ecosystem, so it lives at the public edge (ADR 0032); kernel, core and
// service never learn the word.
//
// # Quick start
//
//	lg, err := logger.NewText(logger.Config{Writer: os.Stderr, MinLevel: logger.LevelInfo})
//	if err != nil { return err }
//
//	sl, err := slogbridge.New(lg)
//	if err != nil { return err }
//
//	srv := mcp.NewServer(impl, &mcp.ServerOptions{Logger: sl})
//
// Every record the foreign library emits through sl now travels the SDK
// pipeline: filtered at the SDK Logger's threshold, rendered by the SDK
// encoder, decorated with "framework_version".
//
// # Level mapping is exact
//
// The two scales coincide by construction — SDK Debug/Info/Warn/Error are
// -4/0/4/8, the same integers slog uses — so the conversion is lossless and
// intermediate values (slog.Level(2)) survive. Only levels outside the int8
// range saturate, since the SDK Level is an int8.
//
// A single consequence worth stating: the SDK Logger's threshold is now the
// ONLY threshold. A caller who used to build the slog view with its own
// slog.HandlerOptions.Level must move that decision to the SDK Logger's
// MinLevel; the bridge deliberately offers no second knob to disagree with.
//
// # Faithfulness and its one limit
//
// Attributes convert by Kind: String, Int64, Uint64, Float64, Bool, Duration
// and Time map onto their SDK peers, keeping the encoders' type-aware
// rendering. slog.KindAny is the deliberate exception — both bundled encoders
// print "?" for an Any payload, so an slog.Any("err", err) routed through it
// would arrive with its value erased. The bridge converts such a value with
// [log/slog.Value.String], which formats it exactly as slog's own handlers do:
// the Go type is lost, where a "?" would lose the type and the value both.
//
// Groups flatten to dotted keys ("g1.g2.key"), matching what the SDK text and
// JSON encoders already produce for nested attributes. slog's elision rules
// are honoured — an empty Attr is dropped, an empty group NAME inlines its
// members, and a group with no members is dropped. An empty KEY is a different
// thing and keeps its separator ("g."), which is what slog itself prints.
// [log/slog.LogValuer] values are resolved before conversion.
//
// Two things do NOT cross, both because the SDK's Logger.Log contract carries
// neither. slog.Record.PC is the first: the bridge calls the ordinary Log,
// which captures a program counter at the bridge, so a destination built with
// [logger.WithCaller] reports this package as the source rather than the
// foreign library's own logging call. Do not enable WithCaller on a
// bridged destination — a wrong source is worse than none.
//
// The second is slog.Record.Time. The SDK's Logger.Log
// contract takes no timestamp, so the record is stamped by the SDK handler's
// clock at emit time instead. For live logging the difference is sub-
// microsecond; for a REPLAYED record (one built now and handled later) the
// original time is lost. Callers replaying records should carry the original
// instant as an explicit attribute.
package slogbridge
