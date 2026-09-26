// Package static — what NewHandler refuses in a Config.
package static

import (
	"strings"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/kernel/errs"
)

// fieldOption names the Config field — or "fs" — a refusal is about.
const fieldOption string = "option"

// asciiDelete is DEL, the one control character above the printable range.
const asciiDelete byte = 0x7f

// headerValueSafe reports whether value can be sent as a header value as it
// is: no control character but a tab. net/http would rewrite a CR or an LF
// into a space and send a policy nobody wrote; the other control characters
// make a value a browser discards.
func headerValueSafe(value string) bool {
	//: byte by byte: every control character is ASCII.
	for index := range len(value) {
		character := value[index]
		//: a control character, the tab excepted, or DEL.
		if (character < ' ' && character != '\t') || character == asciiDelete {
			return false
		}
	}
	//: sendable as written.
	return true
}

// referrerPolicyKnown reports whether value is a Referrer-Policy a browser
// applies: one token the specification defines, or a comma-separated list of
// them (a browser applies the last it knows — the list is how a newer token
// falls back to an older one). An empty item is refused with the rest: it is
// a typo more often than a policy.
func referrerPolicyKnown(value string) bool {
	//: every item must be a token the specification defines.
	for item := range strings.SplitSeq(value, ",") {
		//: the eight, and nothing else.
		switch strings.Trim(item, " \t") {
		//: a defined token.
		case "no-referrer", "no-referrer-when-downgrade", "origin", "origin-when-cross-origin",
			"same-origin", "strict-origin", "strict-origin-when-cross-origin", "unsafe-url":
			continue
		//: an unknown token, a misspelling, a wrong case, an empty item.
		default:
			return false
		}
	}
	//: every item known.
	return true
}

// misconfigured refuses a Config, naming the option and never its value.
func misconfigured(option string) error {
	//: the core sentinel, with the option it is about.
	return errs.Wrap(corenet.StaticMisconfigured, errs.WrapParams{}, errs.String(fieldOption, option))
}
