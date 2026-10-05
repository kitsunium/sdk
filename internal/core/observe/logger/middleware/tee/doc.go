// Package tee declares the codes and sentinels of the logger's dead-letter
// tee middleware, internal/service/observe/logger/middleware/tee: range
// 0.3.29.*, allocated to that engine (ADR 0014 service slot 0x1d) and
// declared here since ADR 0160, so the engine declares none.
//
// Package tee — declares the sentinels the dead-letter tee Sink returns.
// Each var's Reason equals its own name, or its code's, in SCREAMING_SNAKE
// form (ADR 0020).
package tee
