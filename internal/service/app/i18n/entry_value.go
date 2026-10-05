package i18n

// Plain returns an uncounted [EntryValue].
func Plain(text string) EntryValue {
	//: the body, and no declared categories.
	return EntryValue{Pattern: text}
}

// PluralForms returns a counted [EntryValue] whose patterns are keyed by CLDR
// category name.
//
// A non-nil, EMPTY map still marks the entry counted, and is refused at load
// for missing `other` — which is the honest answer to a translator who wrote
// `"cart.items": {}`.
func PluralForms(forms map[string]string) EntryValue {
	//: an explicit non-nil map, so an empty one still declares a counted
	//: message rather than collapsing into an uncounted empty string.
	if forms == nil {
		//: preserve the declaration.
		forms = map[string]string{}
	}
	//: the declared categories.
	return EntryValue{Forms: forms}
}
