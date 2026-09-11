// Package config — how a schema refuses, and what a refusal is allowed to say.
//
// Every message in this file names KEYS and RULES and never a value. A
// configuration value is routinely a password, a token or a connection string,
// and a start-up error is the one message in a service that reaches a log
// aggregator, a terminal and a ticket. The keys are the author's own literals,
// so echoing them is what makes a refusal actionable; the values are the
// operator's, so echoing them is a leak. It is the same rule ADR 0046 states
// for a violation message, applied one layer up, and it has its own test.
package config

import (
	"errors"
	"strings"

	coreconfig "github.com/kitsunium/sdk/internal/core/config"
	corevalidation "github.com/kitsunium/sdk/internal/core/validation"
	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxKeyEchoRunes caps how much of a key a refusal echoes. A key is the
// SCHEMA AUTHOR's own literal, never operator input, so echoing it is what
// makes the refusal actionable — but an unbounded echo turns one bad
// declaration into a log line nobody reads.
const maxKeyEchoRunes int = 96

// maxKeyListRunes caps the joined key list a load failure carries. Keys are
// names, never values, so the echo is safe; a 400-key configuration would still
// produce an unreadable line without the cap.
const maxKeyListRunes int = 240

// keyListSeparator joins the keys a failure names.
const keyListSeparator string = ", "

// echoTruncationMark tells a short list from a shortened one.
const echoTruncationMark string = "…"

// keyFailureKinds is how many independent answers the key pass can carry: the
// keys that were required and absent, and the keys that were supplied and
// unreadable. It is the capacity of the slice they are joined from.
const keyFailureKinds int = 2

// rejectSchema refuses a schema at CONSTRUCTION, naming the key and the clause
// and NEVER the value. A default may be a placeholder credential written in
// source, and a refusal is the one message that reaches a build log.
func rejectSchema(key, clause string) error {
	//: the key and the clause are the two things that make the fix obvious.
	return kerrs.Wrap(coreconfig.ConfigSchemaInvalid, kerrs.WrapParams{},
		kerrs.String("key", clipEcho(key, maxKeyEchoRunes)),
		kerrs.String("clause", clause))
}

// rejectKeys turns the key pass's two answers into one error, or into a genuine
// nil when the deployment satisfied both.
//
// They are joined rather than sequenced because they are independent facts
// about the same map, and an operator who fixes the missing keys only to be
// told about the typo on the next restart has been made to pay twice for one
// pass. errors.Join keeps both matchable: errs.HasCode walks Unwrap() []error,
// so HasCode(err, CodeConfigKeyMissing) and HasCode(err, CodeConfigUnknownKey)
// both answer for the same value.
func rejectKeys(missing, unknown []string) error {
	//: at most two, and usually none.
	failures := make([]error, 0, keyFailureKinds)
	//: the keys the deployment forgot.
	if len(missing) > 0 {
		//: one error naming all of them.
		failures = append(failures, rejectKeyList(coreconfig.ConfigKeyMissing, "missing", missing))
	}
	//: the keys nothing reads.
	if len(unknown) > 0 {
		//: one error naming all of them.
		failures = append(failures, rejectKeyList(coreconfig.ConfigUnknownKey, "unknown", unknown))
	}
	//: one failure travels ALONE rather than inside a join. errs.FieldsOf
	//: reads the first *Error it finds in the tree, so wrapping a lone failure
	//: would cost nothing and gain nothing; and when both legs are present a
	//: caller reading fields must walk them anyway, which is stated here
	//: rather than discovered.
	switch len(failures) {
	//: the deployment satisfied both questions.
	case 0:
		//: a genuine nil, not a non-nil interface holding nothing.
		return nil
	//: the ordinary failure: one kind of key problem.
	case 1:
		//: hand it back untouched, fields and code directly readable.
		return failures[0]
	//: both kinds at once.
	default:
		//: Join keeps both matchable — errs.HasCode walks Unwrap() []error.
		return errors.Join(failures...)
	}
}

// rejectKeyList builds one typed failure naming every key it carries, and the
// count, and nothing else.
func rejectKeyList(sentinel error, counter string, keys []string) error {
	//: the count survives the truncation the list may suffer, so a clipped
	//: message still says how much was clipped.
	return kerrs.Wrap(sentinel, kerrs.WrapParams{},
		kerrs.Int(counter, len(keys)),
		kerrs.String("keys", clipEcho(strings.Join(keys, keyListSeparator), maxKeyListRunes)))
}

// rejectViolations turns a non-empty report into the load's documented
// CONFIG_VALIDATION_FAILED, carrying the count, the first rule and the
// offending KEYS — never a message and never a value. The full report stays
// available through SchemaValue.Check.
//
// The sentinel is the same one a Validator failure surfaces, on purpose: from
// the operator's side the two are one event — "this configuration was refused"
// — and splitting them would make every caller match two codes to ask one
// question.
func rejectViolations(report corevalidation.ReportValue) error {
	//: the first violation identifies the report in one line of a log.
	first := report[0]
	//: fields carry locations only; Public stays the sentinel's fixed literal.
	return kerrs.Wrap(coreconfig.ConfigValidationFailed, kerrs.WrapParams{},
		kerrs.Int("violations", len(report)),
		kerrs.String("rule", first.Rule),
		kerrs.String("keys", clipEcho(strings.Join(report.Paths(), keyListSeparator), maxKeyListRunes)))
}

// clipEcho shortens text to limit runes, marking the truncation. It counts
// RUNES, not bytes, so a non-ASCII key is never cut mid-character.
func clipEcho(text string, limit int) string {
	//: the common case is short enough to travel whole.
	runes := []rune(text)
	//: no truncation needed.
	if len(runes) <= limit {
		//: hand it back unchanged.
		return text
	}
	//: cut on a rune boundary and mark it.
	return string(runes[:limit]) + echoTruncationMark
}
