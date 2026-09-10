// Package i18n — the resolution chain a [Printer] walks.
package i18n

import corei18n "github.com/kitsunium/sdk/internal/core/i18n"

// link is one step of a [Printer]'s resolution chain: a language to look in,
// paired with the plural rules of THAT language.
//
// Pairing them is the whole design. When the English message answers a Polish
// request, the form must be selected with English rules, because the English
// message carries `one` and `other` and nothing else — see the package comment
// of internal/core/i18n.
type link struct {
	// tag is the language to look the key up in.
	tag corei18n.TagValue
	// rules are that language's CLDR plural rules, resolved once.
	rules PluralValue
}

// buildChain assembles the resolution order and resolves each step's rules.
func buildChain(catalog corei18n.Catalog, tag corei18n.TagValue) (chain []link, err error) {
	//: the requested language and its parents.
	chain, err = appendLineage(nil, tag)
	//: an unsupported language stops construction.
	if err != nil {
		//: refuse.
		return nil, err
	}
	//: a catalog that names a fallback extends the chain; one that does not
	//: implement the sibling simply has none — see ADR 0039.
	fallbacker, ok := catalog.(corei18n.Fallbacker)
	//: no fallback.
	if !ok {
		//: the requested lineage is the whole chain.
		return chain, nil
	}
	//: the fallback and its parents come last.
	return appendLineage(chain, fallbacker.Fallback())
}

// appendLineage appends tag and each of its parents to chain, skipping any
// already present and resolving each one's plural rules.
func appendLineage(chain []link, tag corei18n.TagValue) (extended []link, err error) {
	//: a zero tag contributes nothing; a catalog is free not to have one.
	for current := tag; !current.IsZero(); {
		//: every step must have rules, because any step may be the one that
		//: answers and its rules are the ones that will select the form.
		rules, rulesErr := requireRules(current)
		//: UnsupportedLanguage, naming the language and the supported set.
		if rulesErr != nil {
			//: refuse.
			return nil, rulesErr
		}
		//: a language reached twice — "fr" as both a parent and the
		//: fallback — is looked up once.
		if !hasLink(chain, current) {
			//: append it.
			chain = append(chain, link{tag: current, rules: rules})
		}
		//: climb one subtag.
		parent, more := current.Parent()
		//: a bare language has no parent.
		if !more {
			//: lineage complete.
			break
		}
		//: continue with the parent.
		current = parent
	}
	//: the extended chain.
	return chain, nil
}

// hasLink reports whether chain already looks tag up.
func hasLink(chain []link, tag corei18n.TagValue) bool {
	//: a linear scan over at most a handful of steps.
	for _, step := range chain {
		//: TagValue is comparable.
		if step.tag == tag {
			//: present.
			return true
		}
	}
	//: absent.
	return false
}
