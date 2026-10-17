package kit

import (
	ikit "github.com/kitsunium/sdk/framework/internal/kit"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

const (
	// WireInvalid is WireInvalid.
	WireInvalid string = ikit.WireInvalid

	// WireNotFound is WireNotFound.
	WireNotFound string = ikit.WireNotFound

	// WireConflict is WireConflict.
	WireConflict string = ikit.WireConflict

	// WireUnauth is WireUnauth.
	WireUnauth string = ikit.WireUnauth

	// WireForbidden is WireForbidden.
	WireForbidden string = ikit.WireForbidden

	// WireTooLarge is WireTooLarge.
	WireTooLarge string = ikit.WireTooLarge

	// WireUnsupported is WireUnsupported.
	WireUnsupported string = ikit.WireUnsupported

	// WireRateLimited is WireRateLimited.
	WireRateLimited string = ikit.WireRateLimited

	// WireUnavailable is WireUnavailable.
	WireUnavailable string = ikit.WireUnavailable

	// WireTimeout is WireTimeout.
	WireTimeout string = ikit.WireTimeout

	// WireCanceled is WireCanceled.
	WireCanceled string = ikit.WireCanceled

	// WireInternal is WireInternal.
	WireInternal string = ikit.WireInternal

	// CodeStoreDecode is CodeStoreDecode.
	CodeStoreDecode errs.Code = ikit.CodeStoreDecode

	// CodeStoreEncode is CodeStoreEncode.
	CodeStoreEncode errs.Code = ikit.CodeStoreEncode

	// CodeStorePersist is CodeStorePersist.
	CodeStorePersist errs.Code = ikit.CodeStorePersist

	// CodeStoreLoad is CodeStoreLoad.
	CodeStoreLoad errs.Code = ikit.CodeStoreLoad

	// CodeStoreIndex is CodeStoreIndex.
	CodeStoreIndex errs.Code = ikit.CodeStoreIndex

	// CodeTopicEncode is CodeTopicEncode.
	CodeTopicEncode errs.Code = ikit.CodeTopicEncode

	// CodeTopicPublish is CodeTopicPublish.
	CodeTopicPublish errs.Code = ikit.CodeTopicPublish

	// CodeQueueOpen is CodeQueueOpen.
	CodeQueueOpen errs.Code = ikit.CodeQueueOpen

	// CodeUndecodable is CodeUndecodable.
	CodeUndecodable errs.Code = ikit.CodeUndecodable

	// CodeHandlerPanic is CodeHandlerPanic.
	CodeHandlerPanic errs.Code = ikit.CodeHandlerPanic

	// CodeNotMounted is CodeNotMounted.
	CodeNotMounted errs.Code = ikit.CodeNotMounted

	// CodeWorkflowLoad is CodeWorkflowLoad.
	CodeWorkflowLoad errs.Code = ikit.CodeWorkflowLoad

	// A workflow hook panicked: an OnEnter hook fails its transition with it,
	// an OnTransition hook is reported and the transition stands.
	CodeWorkflowHookPanic errs.Code = ikit.CodeWorkflowHookPanic

	// An OnEnter hook changed the state its entity was entering, or the
	// entity's key: the transition is refused.
	CodeWorkflowHookChange errs.Code = ikit.CodeWorkflowHookChange

	// An OnEnter hook fired its own workflow: refused, where it would wait
	// for itself.
	CodeWorkflowReentrant errs.Code = ikit.CodeWorkflowReentrant

	// CodeAppConfig is CodeAppConfig.
	CodeAppConfig errs.Code = ikit.CodeAppConfig

	// CodeAppRunning is CodeAppRunning.
	CodeAppRunning errs.Code = ikit.CodeAppRunning

	// CodeAppListen is CodeAppListen.
	CodeAppListen errs.Code = ikit.CodeAppListen

	// Mailers.
	CodeMailConfig errs.Code = ikit.CodeMailConfig

	// CodeMailEncode is CodeMailEncode.
	CodeMailEncode errs.Code = ikit.CodeMailEncode

	// CodeMailQueue is CodeMailQueue.
	CodeMailQueue errs.Code = ikit.CodeMailQueue

	// Deprecated: kit no longer returns it. The outbox is the SDK's mail
	// spool, which dead-letters a record that does not decode itself
	// (MessageUndecodable, in pkg/v1/app/mail/spool).
	CodeMailUndecodable errs.Code = ikit.CodeMailUndecodable

	// CodeMailPanic is CodeMailPanic.
	CodeMailPanic errs.Code = ikit.CodeMailPanic

	// Loops.
	CodeLoopPanic errs.Code = ikit.CodeLoopPanic

	// CodeLoopNotMounted is CodeLoopNotMounted.
	CodeLoopNotMounted errs.Code = ikit.CodeLoopNotMounted

	// CodeLoopStart is CodeLoopStart.
	CodeLoopStart errs.Code = ikit.CodeLoopStart

	// Secrets.
	CodeSecretStore errs.Code = ikit.CodeSecretStore

	// CodeSecretMissing is CodeSecretMissing.
	CodeSecretMissing errs.Code = ikit.CodeSecretMissing

	// CodeSecretRead is CodeSecretRead.
	CodeSecretRead errs.Code = ikit.CodeSecretRead

	// CodeSecretRotate is CodeSecretRotate.
	CodeSecretRotate errs.Code = ikit.CodeSecretRotate

	// Databases, ADR 0004.
	CodeDatabaseOpen errs.Code = ikit.CodeDatabaseOpen

	// CodeDatabaseMigrate is CodeDatabaseMigrate.
	CodeDatabaseMigrate errs.Code = ikit.CodeDatabaseMigrate

	// CodeDatabaseUnavailable is CodeDatabaseUnavailable.
	CodeDatabaseUnavailable errs.Code = ikit.CodeDatabaseUnavailable

	// CodeDatabaseConfig is CodeDatabaseConfig.
	CodeDatabaseConfig errs.Code = ikit.CodeDatabaseConfig

	// CodeDatabaseVersion refuses the start of a database whose server is below
	// kit's floor: MySQL 8.0.1 or MariaDB 10.6, which lease with SKIP LOCKED.
	CodeDatabaseVersion errs.Code = ikit.CodeDatabaseVersion

	// CodeTransactionSpan is CodeTransactionSpan.
	CodeTransactionSpan errs.Code = ikit.CodeTransactionSpan

	// Privacy (ADR 0006).
	CodePrivacyKey errs.Code = ikit.CodePrivacyKey

	// CodePrivacyErase is CodePrivacyErase.
	CodePrivacyErase errs.Code = ikit.CodePrivacyErase

	// CodePrivacyJournal is CodePrivacyJournal.
	CodePrivacyJournal errs.Code = ikit.CodePrivacyJournal

	// CodePrivacyData is CodePrivacyData.
	CodePrivacyData errs.Code = ikit.CodePrivacyData

	// History (ADR 0007).
	CodeHistoryLoad errs.Code = ikit.CodeHistoryLoad

	// CodeHistoryWrite is CodeHistoryWrite.
	CodeHistoryWrite errs.Code = ikit.CodeHistoryWrite

	// CodeHistoryRead is CodeHistoryRead.
	CodeHistoryRead errs.Code = ikit.CodeHistoryRead

	// CodePasswordHash is CodePasswordHash.
	CodePasswordHash errs.Code = ikit.CodePasswordHash

	// Commands and queries.
	CodeCommandReentrant errs.Code = ikit.CodeCommandReentrant

	// CodeCommandKey is CodeCommandKey.
	CodeCommandKey errs.Code = ikit.CodeCommandKey

	// CodeCommandQueue is CodeCommandQueue.
	CodeCommandQueue errs.Code = ikit.CodeCommandQueue

	// CodeCommandEncode is CodeCommandEncode.
	CodeCommandEncode errs.Code = ikit.CodeCommandEncode

	// Watches (ADR 0008).
	CodeWatchQueue errs.Code = ikit.CodeWatchQueue

	// CodeRecordsKeys refuses [Records].Keys to a caller none of whose module's
	// watches the store feeds: only a module that watches a store lists its
	// keys (ADR 0178 C6).
	CodeRecordsKeys errs.Code = ikit.CodeRecordsKeys

	// CodeWatchReplay refuses `<product> watch replay WATCH` when WATCH names no
	// watch of the product: the command replays a watch the product declares,
	// and nothing else (ADR 0178 C6).
	CodeWatchReplay errs.Code = ikit.CodeWatchReplay

	// Sealing at rest (ADR 0006, step 3).
	CodeSealKey errs.Code = ikit.CodeSealKey

	// CodeSealWrite is CodeSealWrite.
	CodeSealWrite errs.Code = ikit.CodeSealWrite

	// CodeSealOpen is CodeSealOpen.
	CodeSealOpen errs.Code = ikit.CodeSealOpen

	// CodeSealRewrap is CodeSealRewrap.
	CodeSealRewrap errs.Code = ikit.CodeSealRewrap

	// CodeSealShred is CodeSealShred.
	CodeSealShred errs.Code = ikit.CodeSealShred

	// Revisions (ADR 0007 §3).
	CodeRevisionRead errs.Code = ikit.CodeRevisionRead

	// CodeRevisionWrite is CodeRevisionWrite.
	CodeRevisionWrite errs.Code = ikit.CodeRevisionWrite

	// CodeRevisionDecode is CodeRevisionDecode.
	CodeRevisionDecode errs.Code = ikit.CodeRevisionDecode

	// CodeTrash is a store's trash that cannot be read or emptied: what the
	// product deleted stays in it until a later purge (ADR 0178 C4).
	CodeTrash errs.Code = ikit.CodeTrash

	// CodeTrashList refuses the Studio's list of a store's trash (GET
	// /_kit/api/trash) when the store named is none of the product's — a
	// NotFound — or keeps no trash, having no kit.Revisions — an Invalid (ADR
	// 0178 C4).
	CodeTrashList errs.Code = ikit.CodeTrashList

	// The framework's own signals and refusals, typed since the move into the
	// SDK (rule 2: no fmt.Errorf, no errors.New in production code).
	CodeJobOverlapped errs.Code = ikit.CodeJobOverlapped

	// CodeHistoryMoved is CodeHistoryMoved.
	CodeHistoryMoved errs.Code = ikit.CodeHistoryMoved

	// CodeNotSource is CodeNotSource.
	CodeNotSource errs.Code = ikit.CodeNotSource

	// CodePasswordMoved is CodePasswordMoved.
	CodePasswordMoved errs.Code = ikit.CodePasswordMoved

	// CodeNothingToErase is CodeNothingToErase.
	CodeNothingToErase errs.Code = ikit.CodeNothingToErase

	// CodeKeyRekeyed is CodeKeyRekeyed.
	CodeKeyRekeyed errs.Code = ikit.CodeKeyRekeyed

	// CodeMigrateRefused is CodeMigrateRefused.
	CodeMigrateRefused errs.Code = ikit.CodeMigrateRefused

	// CodeCatalogue is CodeCatalogue.
	CodeCatalogue errs.Code = ikit.CodeCatalogue

	// CodeRetentionPanic is CodeRetentionPanic.
	CodeRetentionPanic errs.Code = ikit.CodeRetentionPanic

	// CodeDataCopy refuses a data copy: a database the product does not declare
	// or cannot open, data kept in memory, a product that answers on its
	// address, a table of the database that already holds a row.
	CodeDataCopy errs.Code = ikit.CodeDataCopy

	// CodeDataCopyDiverged reports a data copy whose fingerprints differ: the
	// copy is rolled back, and the first key that differs is named.
	CodeDataCopyDiverged errs.Code = ikit.CodeDataCopyDiverged

	// CodeSubsystemMissing refuses a start whose declarations need a subsystem
	// the program does not import (ADR 0179 §6): the [DiagnosticsError] names
	// each declaration and the package to import, and carries this code.
	CodeSubsystemMissing errs.Code = ikit.CodeSubsystemMissing

	// The signals kit ends one of its own steps with and catches itself — a
	// caller never receives one —, typed when the SDK made rule 2 a gate.
	CodeSealErased errs.Code = ikit.CodeSealErased

	// CodeSealKeyMoved is CodeSealKeyMoved.
	CodeSealKeyMoved errs.Code = ikit.CodeSealKeyMoved

	// CodeResealInPlace is CodeResealInPlace.
	CodeResealInPlace errs.Code = ikit.CodeResealInPlace

	// CodeResealMoved is CodeResealMoved.
	CodeResealMoved errs.Code = ikit.CodeResealMoved

	// CodeWorkflowNotInPlace is CodeWorkflowNotInPlace.
	CodeWorkflowNotInPlace errs.Code = ikit.CodeWorkflowNotInPlace

	// CodeTransactionPanic is CodeTransactionPanic.
	CodeTransactionPanic errs.Code = ikit.CodeTransactionPanic
)

// Error is an error a product returns to its callers. Message travels on the
// wire; the cause attached with [Error].Wrap is logged and never sent.
//
// Return one from a handler to choose the HTTP status and the message the
// caller reads. Any other error becomes a 500 whose body says nothing about
// it: an error message is the classic place a secret leaks from.
type Error = ikit.Error

// Violation is one validation rule a request failed: where, which rule, and
// why — never the value.
type Violation = ikit.ViolationMessage

// Invalid reports a request the caller must change before retrying (400).
func Invalid(message string) *Error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Invalid(message)
}

// NotFound reports that the addressed resource does not exist (404).
func NotFound(message string) *Error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.NotFound(message)
}

// Conflict reports a request the resource's current state refuses (409).
func Conflict(message string) *Error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Conflict(message)
}

// Unauthenticated reports a caller who did not prove who they are (401).
func Unauthenticated(message string) *Error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Unauthenticated(message)
}

// Forbidden reports a caller who may not do this (403).
func Forbidden(message string) *Error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Forbidden(message)
}

// Unavailable reports a transient failure worth retrying later (503).
func Unavailable(message string) *Error {
	//: the implementation is framework/internal/kit's; this facade only forwards.
	return ikit.Unavailable(message)
}
