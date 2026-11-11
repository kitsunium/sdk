//go:build !unix

package loop

// closePacketFD has nothing to close: no platform here hands a lane a
// descriptor, server serving its datagram groups on the batched reader.
func closePacketFD(_ int) {
	//: nothing was handed over.
}
