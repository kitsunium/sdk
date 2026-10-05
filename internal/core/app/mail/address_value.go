package mail

// isZero is AddressValue.IsZero's body: decl_gen.go writes AddressValue.IsZero, from the
// design, as one call of it.
func (a AddressValue) isZero() bool {
	//: a display name with no mailbox is not an address, it is a label.
	return a.Addr == ""
}
