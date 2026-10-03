// Package config — the KEY pass: which keys a load must find, and which it
// must refuse.
//
// It runs on the MERGED MAP, before the decode, and that position is the whole
// argument. After the decode an absent key and a key set to its zero are the
// same bytes, so no constraint over the decoded value can tell "the operator
// did not configure a port" from "the operator configured port 0". Before it,
// the question is trivial: the key is either there or it is not.
package config

import "slices"

// requiredKey is one key a load must find, pre-split at construction so the
// presence pass parses nothing on the start-up path.
type requiredKey struct {
	// key is the literal the author declared, and the only thing a failure
	// echoes.
	key string
	// segments is that literal already split into nesting levels.
	segments []string
}

// missingKeys returns every required key no layer supplied, in declaration
// order.
//
// It collects them ALL. Reporting the first would make an operator restart the
// service once per missing key to discover the next one, which is the failure a
// report exists to prevent — the same rule ADR 0046 states for violations, and
// the reason a schema is checked in one pass rather than at first access.
func missingKeys(merged map[string]any, required []requiredKey) []string {
	//: nothing required — the common case allocates nothing.
	if len(required) == 0 {
		//: no pass to run.
		return nil
	}
	//: the absences, in the order they were declared.
	var missing []string
	//: every requirement is asked, even after one has already failed.
	for _, key := range required {
		//: an entry that exists satisfies the requirement, whatever it holds.
		if lookupKey(merged, key.segments) {
			//: supplied.
			continue
		}
		//: record and keep going.
		missing = append(missing, key.key)
	}
	//: every key the deployment forgot.
	return missing
}

// unknownKeys returns every supplied key the target type cannot address, sorted
// so the message is the same on every run.
//
// It walks the merged map rather than the type, because the question is about
// what a SOURCE carried. The walk stops at a leaf: below `extra` declared as a
// map[string]any, or below a time.Time, there are no keys — there is one value
// the decoder owns — so descending would report the contents of a legitimate
// value as a typo.
func unknownKeys(merged map[string]any, known map[string]keyKind, strict bool) []string {
	//: the author opted out, so the walk is not run at all.
	if !strict {
		//: nothing to report.
		return nil
	}
	//: the strangers, collected across the whole document.
	var unknown []string
	//: walk from the root, whose base path is the empty prefix.
	collectUnknown(merged, "", known, &unknown)
	//: map iteration order is random, so sort: a message that changes between
	//: two runs of the same deployment cannot be diffed, and a test cannot pin
	//: it.
	slices.Sort(unknown)
	//: every key nothing reads.
	return unknown
}

// collectUnknown appends every unaddressable key below level into out.
func collectUnknown(level map[string]any, base string, known map[string]keyKind, out *[]string) {
	//: every entry the merged document carries at this level.
	for name, value := range level {
		//: the key an operator would have typed to set this entry.
		path := joinKey(base, name)
		//: the type's answer about this key.
		kind, addressable := known[path]
		//: a key outside the vocabulary is the typo the check exists for.
		if !addressable {
			//: record it and do NOT descend: everything below an unknown key
			//: is unknown for the same reason, and naming the parent once is
			//: what an operator can act on.
			*out = append(*out, path)
			//: next entry.
			continue
		}
		//: a table's members are keys too — but only when the document really
		//: supplied a table there. Anything else is the decoder's problem, and
		//: it reports it as CONFIG_DECODE_FAILED with its own diagnosis.
		if sub, isTable := value.(map[string]any); isTable && kind == keyTable {
			//: descend one level.
			collectUnknown(sub, path, known, out)
		}
	}
}
