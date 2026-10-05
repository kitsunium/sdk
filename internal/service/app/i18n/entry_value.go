package i18n

// plain is Plain's body: decl_gen.go writes Plain, from the
// design, as one call of it.
func plain(text string) EntryValue {
	//: the body, and no declared categories.
	return EntryValue{Pattern: text}
}

// pluralForms is PluralForms's body: decl_gen.go writes PluralForms, from the
// design, as one call of it.
func pluralForms(forms map[string]string) EntryValue {
	//: an explicit non-nil map, so an empty one still declares a counted
	//: message rather than collapsing into an uncounted empty string.
	if forms == nil {
		//: preserve the declaration.
		forms = map[string]string{}
	}
	//: the declared categories.
	return EntryValue{Forms: forms}
}
