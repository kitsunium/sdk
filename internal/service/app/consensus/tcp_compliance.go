package consensus

// The node reaches these capabilities of the TCP transport by type
// assertion alone, so a transport that stopped satisfying one would quietly
// lose the behaviour: links no longer bounded by read silence (#1116), their
// aborts no longer counted. Each line turns that into a compile error.
var (
	_ silenceBounder = (*TCPTransport)(nil)
	_ interface {
		linksAborted() (userTimeout, readSilence int64)
	} = (*TCPTransport)(nil)
)
