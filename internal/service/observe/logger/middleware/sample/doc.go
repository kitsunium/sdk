// Package sample implements two sampling Sinks. New is a 1-of-N rate
// limiter: every Nth Write is forwarded to the downstream sink and the rest
// are silently dropped, by one atomic counter over every record.
//
// NewKeyed is the sampler a production stream needs (#1017): a counter per
// key — the record's level and message unless KeyedConfig.Key says
// otherwise — forwards the first First records of each Window, then every
// Thereafter-th, so a burst of one repeated line is thinned while a rare
// line interleaved with it still passes; records at or above
// MinLevelBypass (Warn by default) are never sampled; every drop is counted
// (Dropped) and reported (OnDrop), so sampling is told apart from silence
// (ADR 0031). The window is measured on the configured Clock with no
// goroutine: a key's counter carries its window start and restarts on the
// first Write past it. MaxKeys bounds the table; a key past the bound is
// counted under one overflow counter, so a message carrying an identifier
// cannot grow it.
//
// Use case: emit verbose Debug records at a 1/100 rate to a remote drain
// while keeping the local console stream intact (compose with sink/multi
// from commit 11 for the per-branch policy).
package sample
