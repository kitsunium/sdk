// Package levelgate provides a Sink decorator that forwards records at or above
// a minimum severity and silently drops the rest. It implements the per-writer
// MinLevel floor for the writer factories (console, file, s3, cloudwatch)
// without the route middleware's NoMatch error — a below-threshold record is a
// successful no-op, not a failure, so it never pollutes a multi fan-out.
//
// Two constructors share the one gate. New is the writer configuration's: its
// Info is the zero value of MinLevel and means "inherit the handler's level",
// so New returns the sink unwrapped there. Floor is everyone else's: the floor
// it is given is the floor it applies, Info included (ADR 0132).
package levelgate
