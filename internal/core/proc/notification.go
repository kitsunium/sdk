package proc

// ready is NotificationValue.Ready's body: decl_gen.go writes NotificationValue.Ready, from the
// design, as one call of it.
func (n NotificationValue) ready() bool {
	//: a nil State indexes safely to "" — an absent field is simply not ready.
	return n.State["READY"] == "1"
}

// reloading is NotificationValue.Reloading's body: decl_gen.go writes NotificationValue.Reloading, from the
// design, as one call of it.
func (n NotificationValue) reloading() bool {
	//: a nil State indexes safely to "" — an absent field means not reloading.
	return n.State["RELOADING"] == "1"
}

// stopping is NotificationValue.Stopping's body: decl_gen.go writes NotificationValue.Stopping, from the
// design, as one call of it.
func (n NotificationValue) stopping() bool {
	//: a nil State indexes safely to "" — an absent field means not stopping.
	return n.State["STOPPING"] == "1"
}

// watchdog is NotificationValue.Watchdog's body: decl_gen.go writes NotificationValue.Watchdog, from the
// design, as one call of it.
func (n NotificationValue) watchdog() bool {
	//: a nil State indexes safely to "" — an absent field means no ping.
	return n.State["WATCHDOG"] == "1"
}
