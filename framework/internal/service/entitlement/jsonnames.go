// Package entitlement - refusing a JSON document that names one member twice.
//
// encoding/json accepts {"a":1,"a":2} and keeps the LAST value, silently. The
// roster's signature covers the RAW bytes and is verified before any decoding,
// so this is not a forgery a third party can mount: what it is, is a divergence
// between the party that PUBLISHED a document and the party that READS it. Two
// readers of one byte-identical, correctly signed payload — one keeping the
// first member, one keeping the last — hold different rosters and can both
// prove the vendor signed what they hold.
//
// Refusing is the only reading that cannot contradict anybody. RFC 8725 §2.6
// states the same rule for the JWT case.
package entitlement

import (
	"bytes"
	"encoding/json"
)

// nameFrame is one open JSON container during the scan.
//
// names is allocated only for objects: an array has no member names to collide,
// and allocating a map per array element would dominate the cost of the scan on
// a roster whose subjects are the bulk of the document.
type nameFrame struct {
	// names is every member name seen in this object so far.
	names map[string]struct{}
	// object distinguishes an object frame from an array frame.
	object bool
	// expectKey is true when the next scalar in this object is a member name
	// rather than a value. Only meaningful for an object frame.
	expectKey bool
}

// checkNoDuplicateNames reports the first member name declared twice in one
// JSON object, at ANY depth.
//
// # Why recursive, where internal/service/token's equivalent is not
//
// internal/service/token/encoding.go stops at the top level and skips nested
// values wholesale, which is right for a JWT claim set. It is not enough here:
// Subjects and CIAccounts are map[string]…, so a duplicated subject uuid — one
// entry authorising a key, a second one at the same uuid revoking it — lives
// TWO levels down and a top-level scan never sees it. That case is precisely
// the equivocation this package cannot afford to resolve by accident.
//
// It is not shared with that package for one concrete reason: its refusal
// carries a sentinel in the 0.3.44.* range, and this package may only emit
// 0.3.67.* and the contract's fifteen. Returning a name and a boolean rather
// than an error is what lets each call site refuse under ITS OWN sentinel.
//
// # What it does not do
//
// It does not judge whether the document is well-formed JSON. A malformed one
// is reported as carrying no duplicate, and the json.Unmarshal that follows
// every call site reports the syntax error with the diagnosis it already had.
// Two verdicts on one input, from two places, is the defect that produces
// contradictory refusals.
func checkNoDuplicateNames(data []byte) (duplicate string, found bool) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	//: Depth is bounded by the artefact cap the caller already applied, so the
	//: stack cannot grow past what a capped document can nest.
	var stack []*nameFrame

	for {
		token, tokenErr := decoder.Token()
		//: End of input and malformed input reach the SAME verdict, and
		//: deliberately: a complete scan found nothing, and a syntax error is
		//: the next decoder's diagnosis rather than a second one from here.
		if tokenErr != nil {
			//: No duplicate to report.
			return "", false
		}
		name, seen, next := admitToken(stack, token)
		//: Two readers of this document would disagree.
		if seen {
			//: Report the name; the call site chooses the sentinel.
			return name, true
		}
		stack = next
	}
}

// admitToken folds one token into the frame stack.
//
// Split out of the loop so the scan reads as "fold a token, or stop": the
// member-name case has four conditions of its own, and inlining them next to
// the decoder's own two left one function holding the whole grammar.
func admitToken(stack []*nameFrame, token json.Token) (duplicate string, found bool, next []*nameFrame) {
	delim, isDelim := token.(json.Delim)
	//: A container opens or closes, which is the only thing that moves the
	//: stack. Nothing to compare on a delimiter.
	if isDelim {
		//: Move the stack and carry on.
		return "", false, applyDelim(stack, delim)
	}

	name, isName := token.(string)
	top := topFrame(stack)
	//: A string where a member name is expected IS the member name; every other
	//: scalar, and a string in value position, is a value.
	if isName && top != nil && top.object && top.expectKey {
		//: Seen already in THIS object: two readers would disagree.
		if _, repeated := top.names[name]; repeated {
			//: Report it.
			return name, true, stack
		}
		top.names[name] = struct{}{}
		top.expectKey = false
		//: The value for this name comes next.
		return "", false, stack
	}
	//: A scalar value completes a member, so the next scalar in this object is
	//: a name again.
	valueClosed(top)
	//: Nothing to report.
	return "", false, stack
}

// applyDelim moves the frame stack for one delimiter.
func applyDelim(stack []*nameFrame, delim json.Delim) []*nameFrame {
	//: Dispatch on the delimiter: two of them open a frame and two close one.
	switch delim {
	//: An object opens. Its parent's member is NOT complete until the matching
	//: brace, which is why expectKey is not restored here.
	case '{':
		//: Push an object frame, expecting a name first.
		return append(stack, &nameFrame{names: map[string]struct{}{}, object: true, expectKey: true})
	//: An array opens. No member names, so no map.
	case '[':
		//: Push an array frame.
		return append(stack, &nameFrame{})
	//: A container closes, which completes the member it was the value of.
	default:
		//: A well-formed stream never closes more than it opened, and a
		//: malformed one is the next decoder's verdict — guard rather than
		//: index past the start.
		if len(stack) == 0 {
			//: Nothing open.
			return stack
		}
		stack = stack[:len(stack)-1]
		valueClosed(topFrame(stack))
		//: Back in the parent.
		return stack
	}
}

// topFrame returns the innermost open frame, or nil at the top level.
func topFrame(stack []*nameFrame) *nameFrame {
	//: The top level is not a frame; a caller must tolerate nil.
	if len(stack) == 0 {
		//: No enclosing container.
		return nil
	}
	//: The innermost container.
	return stack[len(stack)-1]
}

// valueClosed records that a complete value was consumed in frame.
func valueClosed(frame *nameFrame) {
	//: Only an object alternates names and values; the top level and arrays
	//: have nothing to restore.
	if frame == nil || !frame.object {
		//: Nothing to record.
		return
	}
	//: The next scalar here is a member name.
	frame.expectKey = true
}
