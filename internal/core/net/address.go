// Package net — the listen address value.
package net

// AddressValue names one socket to bind. It is a value type so a group's listen
// set is comparable and copyable, and so a configuration file can express it
// directly.
type AddressValue struct {
	// Network is the socket family: tcp, tcp4, tcp6, udp, udp4, udp6, unix,
	// unixgram or unixpacket.
	Network string `json:"network"`
	// Addr is the bind target: "host:port" for the IP families, a filesystem
	// path for the unix families.
	Addr string `json:"addr"`
}

// String renders the address as "network://addr" for logs and error fields.
func (a AddressValue) String() string {
	//: a compact single-token form so it reads cleanly as a structured field.
	return a.Network + "://" + a.Addr
}
