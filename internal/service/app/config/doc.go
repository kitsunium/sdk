// Package config — environment-variable Source.
//
// Package config — file Source (codec-dispatched parse).
//
// Package config — file Source over an io/fs.FS (codec-dispatched parse).
//
// Package config — the merge + decode + validate loader.
//
// Package config — recursive layer merge.
//
// Package config — traced loads: the same pipeline, and for every key of the
// target which layer supplied its final value (ADR 0097).
//
// Package config — cross-OS poll watcher.
//
// Package config — the compiled schema: typed defaults, required keys, the
// unknown-key policy, and the constraints the decoded configuration must
// satisfy.
//
// A schema is compiled ONCE and refused at construction when it contradicts
// either the type it describes or itself. Nothing here decides anything at load
// time that could have been decided here.
//
// Package config — resolving a declaration into a compiled schema.
//
// Everything in this file runs once, inside NewSchema, and every failure it
// reports is CONFIG_SCHEMA_INVALID: the author's declaration, not the
// operator's deployment.
//
// Package config — the key grammar a schema declares in, and its resolution
// against the target type.
//
// Everything here runs ONCE, inside NewSchema. Nothing in this file runs during
// a Load: a schema that compiled is a schema whose keys have already been
// proven to name something.
//
// Package config — the KEY pass: which keys a load must find, and which it
// must refuse.
//
// It runs on the MERGED MAP, before the decode, and that position is the whole
// argument. After the decode an absent key and a key set to its zero are the
// same bytes, so no constraint over the decoded value can tell "the operator
// did not configure a port" from "the operator configured port 0". Before it,
// the question is trivial: the key is either there or it is not.
//
// Package config — how a schema refuses, and what a refusal is allowed to say.
//
// Every message in this file names KEYS and RULES and never a value. A
// configuration value is routinely a password, a token or a connection string,
// and a start-up error is the one message in a service that reaches a log
// aggregator, a terminal and a ticket. The keys are the author's own literals,
// so echoing them is what makes a refusal actionable; the values are the
// operator's, so echoing them is a leak. It is the same rule ADR 0046 states
// for a violation message, applied one layer up, and it has its own test.
//
// Package config — the default layer, seen as an ordinary Source.
//
// Package config — the schema declaration: what an author writes.
//
// Package config provides concrete configuration sources (env, file), the
// merge+decode loader, and a cross-OS poll watcher implementing core/app/config.
// Stdlib-only (file parsing dispatches through the codec registry). ADR 0028.
package config
