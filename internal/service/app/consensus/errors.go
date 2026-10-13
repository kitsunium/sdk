package consensus

import (
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

const (
	// errImpossible is the cause of an answer a store must never give.
	errImpossible cause = "impossible answer"

	// errPinned is the cause of a Remove refused while a handle is open.
	errPinned cause = "snapshot pinned by an open handle"
	// errSilenceLength is the cause of a silence condition of a length this
	// version never writes.
	errSilenceLength cause = "silence condition of the wrong length"

	// MaxJoinReasonBytes bounds the reason a JOIN refusal carries to the joiner.
	MaxJoinReasonBytes int = 256
)

// sentinelByCode maps every code the step function may answer to the
// sentinel a caller matches on. A code it does not hold is answered by
// sentinelOf as OUTCOME_UNKNOWN: an outcome the engine cannot name is never
// guessed into a more specific one.
var sentinelByCode = map[errs.Code]*errs.Error{
	coreconsensus.CodeNotLeader:               coreconsensus.NotLeader,
	coreconsensus.CodeMembershipChangePending: coreconsensus.MembershipChangePending,
	coreconsensus.CodeMembershipInvalid:       coreconsensus.MembershipInvalid,
	coreconsensus.CodeNodeIDRetired:           coreconsensus.NodeIDRetired,
	coreconsensus.CodeAdminPreconditionFailed: coreconsensus.AdminPreconditionFailed,
	coreconsensus.CodeTransferFailed:          coreconsensus.TransferFailed,
	coreconsensus.CodeVersionUnsupported:      coreconsensus.VersionUnsupported,
	coreconsensus.CodeConsensusMisconfigured:  coreconsensus.ConsensusMisconfigured,
	coreconsensus.CodeOverloaded:              coreconsensus.Overloaded,
	coreconsensus.CodeEntryTooLarge:           coreconsensus.EntryTooLarge,
	coreconsensus.CodeCompacted:               coreconsensus.Compacted,
	coreconsensus.CodeFrameInvalid:            coreconsensus.FrameInvalid,
	coreconsensus.CodeSnapshotTooLarge:        coreconsensus.SnapshotTooLarge,
	coreconsensus.CodeJoinRefused:             coreconsensus.JoinRefused,
	coreconsensus.CodeNoSpace:                 coreconsensus.NoSpace,
}

// cause is the text of a storage failure's cause. It never travels as an
// error of its own: storageFailed records it in a field of a
// STORAGE_FAILED, the only error a caller matches on.
type cause string

// Error returns the cause's text unchanged, so the field storageFailed
// records reads exactly as the constant was written.
func (c cause) Error() string { return string(c) }

// u64s renders v in decimal: the spelling every index field of an error
// carries, and the one LeaderHint parses back.
func u64s(v uint64) string { return strconv.FormatUint(v, 10) }

// origin builds a fresh error on its own sentinel: every outcome is an
// ORIGIN, its cause kept as a field, never a wrap of the cause (R7-10).
func origin(sentinel *errs.Error, fields ...errs.FieldValue) error {
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

// alsoFailed returns err, the failure being reported, with the failure of the
// cleanup that followed it kept beside it. The cause stays the error a caller
// matches on — a cleanup's failure must not replace the cause with a
// consequence — and the cleanup's failure is still there to be read: an SDK
// error gains a field and keeps its single line, any other error is joined,
// so errs.HasCode still finds a STORAGE_FAILED the cleanup raised.
func alsoFailed(err error, op string, failure error) error {
	if failure == nil || err == nil {
		return err
	}
	if _, ok := errs.CodeOf(err); ok {
		return errs.Wrap(err, errs.WrapParams{}, errs.String(op, failure.Error()))
	}
	return errors.Join(err, failure)
}

// abandon is where an error with no reader goes, and the only place one may.
// Its callers are cleanups whose outcome is already decided: closing a socket
// the transport is dropping, writing the last reply on a connection whose
// peer reads the failure itself, closing a temporary file no one will read
// and recovery deletes. Each call site says which. It is never handed the
// error of a write, a sync or a read of durable state: those stop the node
// (D4) or reach the caller.
func abandon(err error) {
	if err == nil {
		return
	}
	// The resource is gone whatever the error says, and nothing downstream
	// could act on it.
}

// storageFailed is the STORAGE_FAILED of operation op, its cause kept as a
// field rather than wrapped, so the code a caller matches on is always the
// storage failure and never whatever the cause happened to carry (D4).
func storageFailed(op string, cause error) error {
	f := []errs.FieldValue{errs.String("op", op)}
	if cause != nil {
		f = append(f, errs.String("cause", cause.Error()))
	}
	return origin(coreconsensus.StorageFailed, f...)
}

// notLeader is the NOT_LEADER a follower answers, the leader it knows in
// the field LeaderHint reads; a zero hint means none is known.
func notLeader(hint coreconsensus.NodeID) error {
	return origin(coreconsensus.NotLeader, errs.String("leader", hint.String()))
}

// outcomeUnknown is the OUTCOME_UNKNOWN of an operation that may or may not
// have committed: the caller must not retry it as if it had failed.
func outcomeUnknown(why string) error {
	return origin(coreconsensus.OutcomeUnknown, errs.String("why", why))
}

// withToken is an error on sentinel carrying the committed token t in the
// hex field TokenOf decodes.
func withToken(sentinel *errs.Error, t coreconsensus.TokenValue) error {
	return origin(sentinel, errs.String("token", hex.EncodeToString(wire.AppendToken(nil, t))))
}

// sentinelOf returns the sentinel of a code the step function answered,
// OUTCOME_UNKNOWN for any code it does not name.
func sentinelOf(c errs.Code) *errs.Error {
	if s, ok := sentinelByCode[c]; ok {
		return s
	}
	return coreconsensus.OutcomeUnknown
}

// LeaderHint reads the known leader from a NOT_LEADER's field (R7-10).
func LeaderHint(err error) (coreconsensus.NodeID, bool) {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "leader" {
			v, perr := strconv.ParseUint(f.StringValue(), 10, 64)
			if perr == nil && v != 0 {
				return coreconsensus.NodeID(v), true
			}
		}
	}
	return 0, false
}

// TokenOf reads the committed token an error carries (R7-10).
func TokenOf(err error) (coreconsensus.TokenValue, bool) {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "token" {
			b, herr := hex.DecodeString(f.StringValue())
			if herr != nil {
				return coreconsensus.TokenValue{}, false
			}
			t, perr := wire.ParseToken(b)
			return t, perr == nil
		}
	}
	return coreconsensus.TokenValue{}, false
}

// joinReasonOf is what a joiner may read of an authorizer's refusal: an
// SDK error's public message, else the error's text, printable UTF-8 only,
// trimmed, and cut at MaxJoinReasonBytes on a rune boundary. It holds
// nothing the authorizer did not choose to say.
func joinReasonOf(err error) string {
	msg := err.Error()
	if _, ok := errs.CodeOf(err); ok {
		msg = errs.PublicOf(err)
	}
	return sanitizeReason(msg)
}

// sanitizeReason keeps the printable runes of s, trimmed, at most
// MaxJoinReasonBytes long.
func sanitizeReason(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			continue
		}
		if b.Len()+utf8.RuneLen(r) > MaxJoinReasonBytes {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// JoinRefusalReason reads the reason a refused JOIN carried back to the
// joiner: what the member's JoinAuthorizer chose to say, or the engine's
// rule. false when the refusal carried none.
func JoinRefusalReason(err error) (string, bool) {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "reason" && f.StringValue() != "" {
			return f.StringValue(), true
		}
	}
	return "", false
}

// joinRefused builds the joiner's JOIN_REFUSED, its reason in a field.
func joinRefused(code errs.Code, reason string) error {
	reason = sanitizeReason(reason)
	if reason == "" {
		return origin(sentinelOf(code))
	}
	return origin(sentinelOf(code), errs.String("reason", reason))
}
