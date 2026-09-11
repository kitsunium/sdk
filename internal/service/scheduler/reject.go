// Package scheduler — the parser's refusal helpers. Every rejection names what
// was wrong in structured fields; the Public message stays a fixed literal.
package scheduler

import (
	"strconv"
	"time"

	kerrs "github.com/kitsunium/sdk/internal/kernel/errs"
)

// maxEchoRunes caps how much of the caller's own text a refusal echoes into a
// field. The expression is the caller's configuration, not untrusted input, so
// echoing it is what makes the error actionable — but an unbounded echo turns
// one bad config line into a log entry nobody can read.
const maxEchoRunes int = 64

// rejectExpression refuses a malformed or out-of-range cron field.
func rejectExpression(detail, item, extra string) error {
	//: detail says what is wrong, item shows the offending text, extra carries
	//: whatever the specific check knows (an accepted range, a field count).
	return kerrs.Wrap(InvalidExpression, kerrs.WrapParams{},
		kerrs.String("detail", detail), kerrs.String("item", clip(item)),
		kerrs.String("context", extra))
}

// rejectSyntax refuses a construct from another cron dialect, by name.
func rejectSyntax(detail, item string) error {
	//: naming the dialect is the point — "not valid" would leave the caller
	//: guessing whether their expression is wrong or merely unsupported here.
	return kerrs.Wrap(UnsupportedSyntax, kerrs.WrapParams{},
		kerrs.String("detail", detail), kerrs.String("item", clip(item)))
}

// rejectUnreachable refuses a valid expression that matches no instant.
func rejectUnreachable(expr string) error {
	//: the horizon is part of the claim: "no match in N years" is falsifiable,
	//: "never matches" is not.
	return kerrs.Wrap(UnreachableSchedule, kerrs.WrapParams{},
		kerrs.String("expression", clip(expr)),
		kerrs.Int("horizon_years", horizonYears))
}

// rejectLocation refuses a nil *time.Location.
func rejectLocation() error {
	//: no field to add — the fault is the absence of the argument itself.
	return kerrs.Wrap(InvalidLocation, kerrs.WrapParams{})
}

// rejectInterval refuses a non-positive Every period.
func rejectInterval(period time.Duration) error {
	//: a duration is the caller's own literal, so echoing it is safe and it is
	//: the one thing that makes the refusal self-explanatory.
	return kerrs.Wrap(InvalidInterval, kerrs.WrapParams{},
		kerrs.String("period", period.String()))
}

// clip shortens text to maxEchoRunes runes, marking the truncation. It counts
// RUNES, not bytes, so a multi-byte expression is never cut mid-character.
func clip(text string) string {
	//: convert once; cron expressions are short, so this is not a hot path.
	runes := []rune(text)
	//: short enough to travel whole.
	if len(runes) <= maxEchoRunes {
		//: nothing to do.
		return text
	}
	//: mark the cut so a reader never mistakes the prefix for the whole value.
	return string(runes[:maxEchoRunes]) + "…"
}

// rangeText renders an inclusive bound pair as "min-max" for an error field.
func rangeText(minValue, maxValue int) string {
	//: strconv rather than fmt keeps the parser's imports to what it parses.
	return strconv.Itoa(minValue) + "-" + strconv.Itoa(maxValue)
}
