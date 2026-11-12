package server

const (
	// reuseNone binds without SO_REUSEPORT: one socket holds the address.
	reuseNone reuseMode = iota
	// reuseShards sets SO_REUSEPORT for the group's own shards, the address
	// probed first (#1330).
	reuseShards
	// reuseShared sets it for another process to join too: ShareAddress
	// (#1330).
	reuseShared
)

// reuseMode is whether a socket is opened with SO_REUSEPORT, and for whom.
type reuseMode uint8

// reuseOf is the mode of an address opened as count sockets, shared with
// other processes or not.
func reuseOf(count int, share bool) reuseMode {
	//: the group asked to share, whatever the count.
	if share {
		//: open to another process.
		return reuseShared
	}
	//: several sockets of our own.
	if count > 1 {
		//: our shards only.
		return reuseShards
	}
	//: one socket, refused by the kernel when held.
	return reuseNone
}
