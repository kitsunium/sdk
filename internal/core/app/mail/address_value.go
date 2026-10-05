package mail

// IsZero reports whether the address carries no addr-spec at all, which is the
// zero value a caller gets from an unfilled struct field.
func (a AddressValue) IsZero() bool {
	//: a display name with no mailbox is not an address, it is a label.
	return a.Addr == ""
}
