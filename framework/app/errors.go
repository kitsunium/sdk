package app

import (
	"time"

	iapp "github.com/kitsunium/sdk/framework/internal/app"
	"github.com/kitsunium/sdk/pkg/v1/errs"
)

const (
	// WireInvalid is WireInvalid.
	WireInvalid string = iapp.WireInvalid

	// WireNotFound is WireNotFound.
	WireNotFound string = iapp.WireNotFound

	// WireConflict is WireConflict.
	WireConflict string = iapp.WireConflict

	// WireUnauth is WireUnauth.
	WireUnauth string = iapp.WireUnauth

	// WireForbidden is WireForbidden.
	WireForbidden string = iapp.WireForbidden

	// WireTooLarge is WireTooLarge.
	WireTooLarge string = iapp.WireTooLarge

	// WireUnsupported is WireUnsupported.
	WireUnsupported string = iapp.WireUnsupported

	// WireRateLimited is WireRateLimited.
	WireRateLimited string = iapp.WireRateLimited

	// WireUnavailable is WireUnavailable.
	WireUnavailable string = iapp.WireUnavailable

	// WireTimeout is WireTimeout.
	WireTimeout string = iapp.WireTimeout

	// WireCanceled is WireCanceled.
	WireCanceled string = iapp.WireCanceled

	// WireInternal is WireInternal.
	WireInternal string = iapp.WireInternal

	// CodeStoreDecode is CodeStoreDecode.
	CodeStoreDecode errs.Code = iapp.CodeStoreDecode

	// CodeStoreEncode is CodeStoreEncode.
	CodeStoreEncode errs.Code = iapp.CodeStoreEncode

	// CodeStorePersist is CodeStorePersist.
	CodeStorePersist errs.Code = iapp.CodeStorePersist

	// CodeStoreLoad is CodeStoreLoad.
	CodeStoreLoad errs.Code = iapp.CodeStoreLoad

	// CodeStoreIndex is CodeStoreIndex.
	CodeStoreIndex errs.Code = iapp.CodeStoreIndex

	// CodeTopicEncode is CodeTopicEncode.
	CodeTopicEncode errs.Code = iapp.CodeTopicEncode

	// CodeTopicPublish is CodeTopicPublish.
	CodeTopicPublish errs.Code = iapp.CodeTopicPublish

	// CodeQueueOpen is CodeQueueOpen.
	CodeQueueOpen errs.Code = iapp.CodeQueueOpen

	// CodeUndecodable is CodeUndecodable.
	CodeUndecodable errs.Code = iapp.CodeUndecodable

	// CodeHandlerPanic is CodeHandlerPanic.
	CodeHandlerPanic errs.Code = iapp.CodeHandlerPanic

	// CodeNotMounted is CodeNotMounted.
	CodeNotMounted errs.Code = iapp.CodeNotMounted

	// CodeWorkflowLoad is CodeWorkflowLoad.
	CodeWorkflowLoad errs.Code = iapp.CodeWorkflowLoad

	// A workflow hook panicked: an OnEnter hook fails its transition with it,
	// an OnTransition hook is reported and the transition stands.
	CodeWorkflowHookPanic errs.Code = iapp.CodeWorkflowHookPanic

	// An OnEnter hook changed the state its entity was entering, or the
	// entity's key: the transition is refused.
	CodeWorkflowHookChange errs.Code = iapp.CodeWorkflowHookChange

	// An OnEnter hook fired its own workflow: refused, where it would wait
	// for itself.
	CodeWorkflowReentrant errs.Code = iapp.CodeWorkflowReentrant

	// CodeAppConfig is CodeAppConfig.
	CodeAppConfig errs.Code = iapp.CodeAppConfig

	// CodeAppRunning is CodeAppRunning.
	CodeAppRunning errs.Code = iapp.CodeAppRunning

	// CodeAppListen is CodeAppListen.
	CodeAppListen errs.Code = iapp.CodeAppListen

	// Mailers.
	CodeMailConfig errs.Code = iapp.CodeMailConfig

	// CodeMailEncode is CodeMailEncode.
	CodeMailEncode errs.Code = iapp.CodeMailEncode

	// CodeMailQueue is CodeMailQueue.
	CodeMailQueue errs.Code = iapp.CodeMailQueue

	// Deprecated: the framework no longer returns it. The outbox is the SDK's mail
	// spool, which dead-letters a record that does not decode itself
	// (MessageUndecodable, in pkg/v1/app/mail/spool).
	CodeMailUndecodable errs.Code = iapp.CodeMailUndecodable

	// CodeMailPanic is CodeMailPanic.
	CodeMailPanic errs.Code = iapp.CodeMailPanic

	// Loops.
	CodeLoopPanic errs.Code = iapp.CodeLoopPanic

	// CodeLoopNotMounted is CodeLoopNotMounted.
	CodeLoopNotMounted errs.Code = iapp.CodeLoopNotMounted

	// CodeLoopStart is CodeLoopStart.
	CodeLoopStart errs.Code = iapp.CodeLoopStart

	// Secrets.
	CodeSecretStore errs.Code = iapp.CodeSecretStore

	// CodeSecretMissing is CodeSecretMissing.
	CodeSecretMissing errs.Code = iapp.CodeSecretMissing

	// CodeSecretRead is CodeSecretRead.
	CodeSecretRead errs.Code = iapp.CodeSecretRead

	// CodeSecretRotate is CodeSecretRotate.
	CodeSecretRotate errs.Code = iapp.CodeSecretRotate

	// Databases, ADR 0004.
	CodeDatabaseOpen errs.Code = iapp.CodeDatabaseOpen

	// CodeDatabaseMigrate is CodeDatabaseMigrate.
	CodeDatabaseMigrate errs.Code = iapp.CodeDatabaseMigrate

	// CodeDatabaseUnavailable is CodeDatabaseUnavailable.
	CodeDatabaseUnavailable errs.Code = iapp.CodeDatabaseUnavailable

	// CodeDatabaseConfig is CodeDatabaseConfig.
	CodeDatabaseConfig errs.Code = iapp.CodeDatabaseConfig

	// CodeDatabaseVersion refuses the start of a database whose server is below
	// the framework's floor: MySQL 8.0.1 or MariaDB 10.6, which lease with SKIP LOCKED.
	CodeDatabaseVersion errs.Code = iapp.CodeDatabaseVersion

	// CodeTransactionSpan is CodeTransactionSpan.
	CodeTransactionSpan errs.Code = iapp.CodeTransactionSpan

	// Privacy (ADR 0006).
	CodePrivacyKey errs.Code = iapp.CodePrivacyKey

	// CodePrivacyErase is CodePrivacyErase.
	CodePrivacyErase errs.Code = iapp.CodePrivacyErase

	// CodePrivacyJournal is CodePrivacyJournal.
	CodePrivacyJournal errs.Code = iapp.CodePrivacyJournal

	// CodePrivacyData is CodePrivacyData.
	CodePrivacyData errs.Code = iapp.CodePrivacyData

	// History (ADR 0007).
	CodeHistoryLoad errs.Code = iapp.CodeHistoryLoad

	// CodeHistoryWrite is CodeHistoryWrite.
	CodeHistoryWrite errs.Code = iapp.CodeHistoryWrite

	// CodeHistoryRead is CodeHistoryRead.
	CodeHistoryRead errs.Code = iapp.CodeHistoryRead

	// CodePasswordHash is CodePasswordHash.
	CodePasswordHash errs.Code = iapp.CodePasswordHash

	// Commands and queries.
	CodeCommandReentrant errs.Code = iapp.CodeCommandReentrant

	// CodeCommandKey is CodeCommandKey.
	CodeCommandKey errs.Code = iapp.CodeCommandKey

	// CodeCommandQueue is CodeCommandQueue.
	CodeCommandQueue errs.Code = iapp.CodeCommandQueue

	// CodeCommandEncode is CodeCommandEncode.
	CodeCommandEncode errs.Code = iapp.CodeCommandEncode

	// Watches (ADR 0008).
	CodeWatchQueue errs.Code = iapp.CodeWatchQueue

	// CodeRecordsKeys refuses [Records].Keys to a caller none of whose module's
	// watches the store feeds: only a module that watches a store lists its
	// keys (ADR 0178 C6).
	CodeRecordsKeys errs.Code = iapp.CodeRecordsKeys

	// CodeWatchReplay refuses `<product> watch replay WATCH` when WATCH names no
	// watch of the product: the command replays a watch the product declares,
	// and nothing else (ADR 0178 C6).
	CodeWatchReplay errs.Code = iapp.CodeWatchReplay

	// Sealing at rest (ADR 0006, step 3).
	CodeSealKey errs.Code = iapp.CodeSealKey

	// CodeSealWrite is CodeSealWrite.
	CodeSealWrite errs.Code = iapp.CodeSealWrite

	// CodeSealOpen is CodeSealOpen.
	CodeSealOpen errs.Code = iapp.CodeSealOpen

	// CodeSealRewrap is CodeSealRewrap.
	CodeSealRewrap errs.Code = iapp.CodeSealRewrap

	// CodeSealShred is CodeSealShred.
	CodeSealShred errs.Code = iapp.CodeSealShred

	// Revisions (ADR 0007 §3).
	CodeRevisionRead errs.Code = iapp.CodeRevisionRead

	// CodeRevisionWrite is CodeRevisionWrite.
	CodeRevisionWrite errs.Code = iapp.CodeRevisionWrite

	// CodeRevisionDecode is CodeRevisionDecode.
	CodeRevisionDecode errs.Code = iapp.CodeRevisionDecode

	// CodeTrash is a store's trash that cannot be read or emptied: what the
	// product deleted stays in it until a later purge (ADR 0178 C4).
	CodeTrash errs.Code = iapp.CodeTrash

	// CodeTrashList refuses the Studio's list of a store's trash (GET
	// /_app/api/trash) when the store named is none of the product's — a
	// NotFound — or keeps no trash, having no app.Revisions — an Invalid (ADR
	// 0178 C4).
	CodeTrashList errs.Code = iapp.CodeTrashList

	// The framework's own signals and refusals, typed since the move into the
	// SDK (rule 2: no fmt.Errorf, no errors.New in production code).
	CodeJobOverlapped errs.Code = iapp.CodeJobOverlapped

	// CodeHistoryMoved is CodeHistoryMoved.
	CodeHistoryMoved errs.Code = iapp.CodeHistoryMoved

	// CodeNotSource is CodeNotSource.
	CodeNotSource errs.Code = iapp.CodeNotSource

	// CodePasswordMoved is CodePasswordMoved.
	CodePasswordMoved errs.Code = iapp.CodePasswordMoved

	// CodeNothingToErase is CodeNothingToErase.
	CodeNothingToErase errs.Code = iapp.CodeNothingToErase

	// CodeKeyRekeyed is CodeKeyRekeyed.
	CodeKeyRekeyed errs.Code = iapp.CodeKeyRekeyed

	// CodeMigrateRefused is CodeMigrateRefused.
	CodeMigrateRefused errs.Code = iapp.CodeMigrateRefused

	// CodeCatalogue is CodeCatalogue.
	CodeCatalogue errs.Code = iapp.CodeCatalogue

	// CodeRetentionPanic is CodeRetentionPanic.
	CodeRetentionPanic errs.Code = iapp.CodeRetentionPanic

	// CodeDataCopy refuses a data copy: a database the product does not declare
	// or cannot open, data kept in memory, a product that answers on its
	// address, a table of the database that already holds a row.
	CodeDataCopy errs.Code = iapp.CodeDataCopy

	// CodeDataCopyDiverged reports a data copy whose fingerprints differ: the
	// copy is rolled back, and the first key that differs is named.
	CodeDataCopyDiverged errs.Code = iapp.CodeDataCopyDiverged

	// CodeSubsystemMissing refuses a start whose declarations need a subsystem
	// the program does not import (ADR 0179 §6): the [DiagnosticsError] names
	// each declaration and the package to import, and carries this code.
	CodeSubsystemMissing errs.Code = iapp.CodeSubsystemMissing

	// CodeMigrationIrreversible refuses the Down of a migration declared
	// irreversible with [Irreversible]: what it changed is undone by a new
	// forward migration, never by migrate down.
	CodeMigrationIrreversible errs.Code = iapp.CodeMigrationIrreversible

	// CodeRenamed refuses a start that meets a runtime name the framework no
	// longer reads (ADR 0180 §3): a KIT_<NAME> variable of the operator's, a
	// field tagged kit:"…". The [DiagnosticsError] names each old name and
	// the one that replaces it, and carries this code.
	CodeRenamed errs.Code = iapp.CodeRenamed

	// CodeRenameTarget refuses a start that meets data a former release kept
	// under the tool's name while its present name is taken too (ADR 0180
	// step 5): nothing is moved, and both names are said.
	CodeRenameTarget errs.Code = iapp.CodeRenameTarget

	// CodeRenameFailed refuses a start whose move of a name at rest did not
	// complete (ADR 0180 step 5): what was not moved stays readable under its
	// former name, and the next start resumes the move.
	CodeRenameFailed errs.Code = iapp.CodeRenameFailed

	// CodeRetryIn marks a failure a handler returned through [RetryIn]: its
	// queue hands the message back with the delay it asked for, the delivery
	// counted.
	CodeRetryIn errs.Code = iapp.CodeRetryIn

	// CodeGaveUp marks a declared loop the framework stopped running: its runs
	// failed more often than its [GiveUpAfter] allows, and the product is no
	// longer live until the process restarts.
	CodeGaveUp errs.Code = iapp.CodeGaveUp

	// CodeOTLPExport refuses a start whose OTLP export ([OTLP]) cannot be
	// opened — its queue, its collector's endpoint, its headers —, says a
	// stop that did not let it flush within its budget, marks a batch the
	// product's own sender refused for good, and refuses the otlp command
	// a queue it cannot read or a dead letter it does not hold.
	CodeOTLPExport errs.Code = iapp.CodeOTLPExport

	// The signals the framework ends one of its own steps with and catches itself — a
	// caller never receives one —, typed when the SDK made rule 2 a gate.
	CodeSealErased errs.Code = iapp.CodeSealErased

	// CodeSealKeyMoved is CodeSealKeyMoved.
	CodeSealKeyMoved errs.Code = iapp.CodeSealKeyMoved

	// CodeResealInPlace is CodeResealInPlace.
	CodeResealInPlace errs.Code = iapp.CodeResealInPlace

	// CodeResealMoved is CodeResealMoved.
	CodeResealMoved errs.Code = iapp.CodeResealMoved

	// CodeWorkflowNotInPlace is CodeWorkflowNotInPlace.
	CodeWorkflowNotInPlace errs.Code = iapp.CodeWorkflowNotInPlace

	// CodeTransactionPanic is CodeTransactionPanic.
	CodeTransactionPanic errs.Code = iapp.CodeTransactionPanic
)

// Error is an error a product returns to its callers. Message travels on the
// wire; the cause attached with [Error].Wrap is logged and never sent.
//
// Return one from a handler to choose the HTTP status and the message the
// caller reads. Any other error becomes a 500 whose body says nothing about
// it: an error message is the classic place a secret leaks from.
type Error = iapp.Error

// Violation is one validation rule a request failed: where, which rule, and
// why — never the value.
type Violation = iapp.ViolationMessage

// Invalid reports a request the caller must change before retrying (400).
func Invalid(message string) *Error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Invalid(message)
}

// NotFound reports that the addressed resource does not exist (404).
func NotFound(message string) *Error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.NotFound(message)
}

// Conflict reports a request the resource's current state refuses (409).
func Conflict(message string) *Error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Conflict(message)
}

// Unauthenticated reports a caller who did not prove who they are (401).
func Unauthenticated(message string) *Error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Unauthenticated(message)
}

// Forbidden reports a caller who may not do this (403).
func Forbidden(message string) *Error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Forbidden(message)
}

// Unavailable reports a transient failure worth retrying later (503).
func Unavailable(message string) *Error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.Unavailable(message)
}

// RetryIn marks cause as a failure to retry in d, no sooner: returned from
// the handler of a subscription, a watch or a queued command, it hands the
// message back to its queue with that delay, in place of the queue's own —
// half a second after a first failure, doubling, never more than a minute —:
// a downstream that answered with a Retry-After, a resource that frees at a
// known instant. The delivery counts as an attempt: a message that used its
// [MaxDeliveries] is dead-lettered with cause, the delay unused. A negative
// d is read as zero.
//
//	if resp.StatusCode == http.StatusTooManyRequests {
//		return app.RetryIn(30*time.Second, errThrottled)
//	}
//
// cause keeps its own code, reason and words — what a dead letter records —
// and [CodeRetryIn] joins its trail; a cause that is no error of the SDK's,
// or none, is recorded as CodeRetryIn itself. Outside a consumer's handler
// it is an error like any other.
func RetryIn(d time.Duration, cause error) error {
	//: the implementation is framework/internal/app's; this facade only forwards.
	return iapp.RetryIn(d, cause)
}
