package consensus

import (
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	coreconsensus "github.com/kitsunium/sdk/internal/core/app/consensus"
	"github.com/kitsunium/sdk/internal/kernel/errs"
	"github.com/kitsunium/sdk/internal/service/app/consensus/wire"
)

// cause is the text of a storage failure's cause. It never travels as an
// error of its own: storageFailed records it in a field of a
// STORAGE_FAILED, the only error a caller matches on.
type cause string

func (c cause) Error() string { return string(c) }

// errImpossible is the cause of an answer a store must never give.
const errImpossible cause = "impossible answer"

func u64s(v uint64) string { return strconv.FormatUint(v, 10) }

// origin builds a fresh error on its own sentinel: every outcome is an
// ORIGIN, its cause kept as a field, never a wrap of the cause (R7-10).
func origin(sentinel *errs.Error, fields ...errs.FieldValue) error {
	return errs.Wrap(sentinel, errs.WrapParams{}, fields...)
}

func storageFailed(op string, cause error) error {
	f := []errs.FieldValue{errs.String("op", op)}
	if cause != nil {
		f = append(f, errs.String("cause", cause.Error()))
	}
	return origin(coreconsensus.StorageFailed, f...)
}

func notLeader(hint coreconsensus.NodeID) error {
	return origin(coreconsensus.NotLeader, errs.String("leader", hint.String()))
}

func outcomeUnknown(why string) error {
	return origin(coreconsensus.OutcomeUnknown, errs.String("why", why))
}

func withToken(sentinel *errs.Error, t coreconsensus.Token) error {
	return origin(sentinel, errs.String("token", hex.EncodeToString(wire.AppendToken(nil, t))))
}

// sentinelOf returns the sentinel of a code the step function answered.
func sentinelOf(c errs.Code) *errs.Error {
	switch c {
	case coreconsensus.CodeNotLeader:
		return coreconsensus.NotLeader
	case coreconsensus.CodeMembershipChangePending:
		return coreconsensus.MembershipChangePending
	case coreconsensus.CodeMembershipInvalid:
		return coreconsensus.MembershipInvalid
	case coreconsensus.CodeNodeIDRetired:
		return coreconsensus.NodeIDRetired
	case coreconsensus.CodeAdminPreconditionFailed:
		return coreconsensus.AdminPreconditionFailed
	case coreconsensus.CodeTransferFailed:
		return coreconsensus.TransferFailed
	case coreconsensus.CodeVersionUnsupported:
		return coreconsensus.VersionUnsupported
	case coreconsensus.CodeConsensusMisconfigured:
		return coreconsensus.ConsensusMisconfigured
	case coreconsensus.CodeOverloaded:
		return coreconsensus.Overloaded
	case coreconsensus.CodeEntryTooLarge:
		return coreconsensus.EntryTooLarge
	case coreconsensus.CodeFrameInvalid:
		return coreconsensus.FrameInvalid
	case coreconsensus.CodeSnapshotTooLarge:
		return coreconsensus.SnapshotTooLarge
	case coreconsensus.CodeJoinRefused:
		return coreconsensus.JoinRefused
	case coreconsensus.CodeNoSpace:
		return coreconsensus.NoSpace
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
func TokenOf(err error) (coreconsensus.Token, bool) {
	for _, f := range errs.FieldsOf(err) {
		if f.Key() == "token" {
			b, herr := hex.DecodeString(f.StringValue())
			if herr != nil {
				return coreconsensus.Token{}, false
			}
			t, perr := wire.ParseToken(b)
			return t, perr == nil
		}
	}
	return coreconsensus.Token{}, false
}

// errPinned is the cause of a Remove refused while a handle is open.
const errPinned cause = "snapshot pinned by an open handle"

// MaxJoinReasonBytes bounds the reason a JOIN refusal carries to the joiner.
const MaxJoinReasonBytes = 256

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
