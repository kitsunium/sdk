package proc

// Ready reports whether the datagram carried READY=1.
func (n NotificationValue) Ready() bool {
	//: a nil State indexes safely to "" — an absent field is simply not ready.
	return n.State["READY"] == "1"
}

// Reloading reports whether the datagram carried RELOADING=1.
func (n NotificationValue) Reloading() bool {
	//: a nil State indexes safely to "" — an absent field means not reloading.
	return n.State["RELOADING"] == "1"
}

// Stopping reports whether the datagram carried STOPPING=1.
func (n NotificationValue) Stopping() bool {
	//: a nil State indexes safely to "" — an absent field means not stopping.
	return n.State["STOPPING"] == "1"
}

// Watchdog reports whether the datagram carried WATCHDOG=1 (a keep-alive ping).
func (n NotificationValue) Watchdog() bool {
	//: a nil State indexes safely to "" — an absent field means no ping.
	return n.State["WATCHDOG"] == "1"
}
