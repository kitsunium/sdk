package storekit

import (
	"errors"
	"strconv"

	"github.com/kitsunium/sdk/framework/internal/kit/plug"
	"github.com/kitsunium/sdk/pkg/v1/data/docstore"
	"github.com/kitsunium/sdk/pkg/v1/data/sql"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

// The SDK's refusals in kit's terms (ADR 0179 §2): every error an engine or
// a database of this package returns goes through said, which tells the
// SDK's sentinels by the kind kit words them by, the SDK's error kept inside
// — its fields, its cause, a deadline it met. kit's core matches the kinds
// and never imports the SDK's document store or sql to match a sentinel.

// sentinels are the SDK's refusals by the kind kit words each by, in the
// order a refusal is told: the first that matches.
var sentinels = []struct {
	sentinel error
	kind     plug.Refusal
}{
	{docstore.KeyTooLong, plug.RefusalKeyTooLong},
	{docstore.WriteUnconfirmed, plug.RefusalUnconfirmed},
	{docstore.DocumentNotFound, plug.RefusalNotFound},
	{docstore.DocumentExists, plug.RefusalExists},
	{docstore.UniqueKeyTaken, plug.RefusalUniqueTaken},
	{docstore.DocumentKeyEmpty, plug.RefusalKeyEmpty},
	{docstore.DocumentKeyChanged, plug.RefusalKeyChanged},
	{docstore.IndexUnknown, plug.RefusalIndexUnknown},
	{docstore.IndexNotUnique, plug.RefusalIndexNotUnique},
	{docstore.IndexBroken, plug.RefusalIndexBroken},
	{docstore.DocumentUndecodable, plug.RefusalUndecodable},
	{docstore.DocumentUnencodable, plug.RefusalUnencodable},
	{docstore.PersistFailed, plug.RefusalPersist},
	{docstore.LoadFailed, plug.RefusalLoad},
	{docstore.StoreClosed, plug.RefusalClosed},
	{docstore.VersionsNotKept, plug.RefusalVersionsNotKept},
	{docstore.VersionsRewriteRefused, plug.RefusalVersionsRewrite},
	{docstore.DocumentNotTrashed, plug.RefusalNotTrashed},
	{docstore.StatementFailed, plug.RefusalStatement},
	{sql.TxClosed, plug.RefusalTxClosed},
	{sql.BeginFailed, plug.RefusalTxFailed},
	{sql.CommitFailed, plug.RefusalTxFailed},
	{sql.RollbackFailed, plug.RefusalTxFailed},
	{sql.SavepointFailed, plug.RefusalTxFailed},
	{sql.TxPoisoned, plug.RefusalTxFailed},
	{sql.MigrationOutOfOrder, plug.RefusalOutOfOrder},
}

// said is err as kit's core reads it: a refusal of the SDK's, as the kind
// kit words it by — a key too long for a database's columns with how long
// they hold —; any other error, and nil, as they are.
func said(err error) error {
	if err == nil {
		return nil
	}
	if r, told := errors.AsType[*plug.RefusalError](err); told && r.Kind != plug.RefusalNone {
		return err
	}
	for _, s := range sentinels {
		if !errors.Is(err, s.sentinel) {
			continue
		}
		if s.kind == plug.RefusalKeyTooLong {
			err = errs.Wrap(err, errs.WrapParams{}, errs.String("max", strconv.Itoa(docstore.MaxSQLKeyLen)))
		}
		return plug.Refuse(s.kind, err)
	}
	return err
}
