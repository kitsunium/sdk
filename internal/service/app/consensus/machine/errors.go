package machine

import (
	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// origin returns sentinel carrying fields: the machine raises the consensus
// domain's own sentinels, so the node that stops on one reports it as it
// reports its own (ADR 0160: the codes stay in the core).
func origin(sentinel *errs.Error, fields ...errs.FieldValue) error {
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

// misconfigured refuses a configuration at construction.
func misconfigured(why string) error {
	return origin(coreconsensus.ConsensusMisconfigured, errs.String("why", why))
}

// failed is an operation of the machine that could not complete: the
// database's failure, or the caller's function's, kept as a field.
func failed(op string, cause error) error {
	f := []errs.FieldValue{errs.String("op", op)}
	if cause != nil {
		f = append(f, errs.String("cause", cause.Error()))
	}
	return origin(coreconsensus.StateMachineFailed, f...)
}

// corrupt is a snapshot whose bytes this machine cannot read.
func corrupt(why string) error {
	return origin(coreconsensus.SnapshotCorrupt, errs.String("why", why))
}

// unsupported is a snapshot of a format version this build does not read:
// the node stops rather than guess (D18).
func unsupported(what string, version byte) error {
	return origin(coreconsensus.VersionUnsupported, errs.String("what", what), errs.Int("version", int(version)))
}
