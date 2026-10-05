package net

// String renders the address as "network://addr" for logs and error fields.
func (a AddressValue) String() string {
	//: a compact single-token form so it reads cleanly as a structured field.
	return a.Network + "://" + a.Addr
}
