package mail_test

import (
	"bufio"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSessionBudget bounds one session, so a wedged test fails instead of
// hanging the suite.
const fakeSessionBudget time.Duration = 10 * time.Second

// fakeOption is what a test chooses about the server it faces.
//
// Every interesting case in this package is a server that advertises the wrong
// thing — no STARTTLS, AUTH over cleartext, a refused RCPT — which is exactly
// what a real relay would not let a test decide.
type fakeOption uint8

const (
	// optSTARTTLS advertises the STARTTLS extension. Omitting it impersonates
	// an attacker who stripped one line from the EHLO response.
	optSTARTTLS fakeOption = 1 << iota
	// optAUTH advertises the AUTH extension.
	optAUTH
	// optImplicitTLS wraps the connection before the greeting (port 465).
	optImplicitTLS
	// optRefuseRcpt answers every RCPT TO with a 550.
	optRefuseRcpt
)

// optDefault is the ordinary submission relay: STARTTLS and AUTH offered.
const optDefault fakeOption = optSTARTTLS | optAUTH

// has reports whether o is set.
func (f fakeOption) has(o fakeOption) bool { return f&o != 0 }

// fakeSMTP is a hand-written SMTP server on a local net.Listener.
//
// It exists because the alternative is a real relay or a container, and neither
// belongs in a unit test. It is the same reasoning that made the sql domain
// write its own driver.Driver.
//
// It records every line it reads in the cleartext phase separately from the
// lines it reads after an upgrade, which is what lets a test assert that a
// credential never crossed an unencrypted socket.
type fakeSMTP struct {
	listener  net.Listener
	options   fakeOption
	tlsConfig *tls.Config

	mutex sync.Mutex
	// clear is every line read before any TLS upgrade.
	clear []string
	// encrypted is every line read after an upgrade.
	encrypted []string
	// data is the DATA payload of the last accepted message.
	data []string
}

// newFakeSMTP starts a server on 127.0.0.1 and returns it with the port it
// listens on.
//
// LIFECYCLE: it starts one accept goroutine, plus one per accepted connection.
// All of them end when the listener closes, which t.Cleanup does at the end of
// the calling test — so none can outlive the test that started it.
func newFakeSMTP(t *testing.T, options fakeOption) (server *fakeSMTP, port int) {
	t.Helper()
	listener, listenErr := net.Listen("tcp", "127.0.0.1:0")
	if listenErr != nil {
		t.Fatalf("listen: %v", listenErr)
	}
	server = &fakeSMTP{listener: listener, options: options, tlsConfig: serverTLSConfig(t)}
	go server.serve()
	t.Cleanup(func() {
		if closeErr := listener.Close(); closeErr != nil {
			t.Logf("closing the fake SMTP listener: %v", closeErr)
		}
	})
	return server, listener.Addr().(*net.TCPAddr).Port
}

// serve accepts connections until the listener is closed.
//
// LIFECYCLE: one goroutine per accepted connection, each ending when its own
// session returns — which every path does, because the connection carries a
// deadline and every read error ends the loop.
func (s *fakeSMTP) serve() {
	for {
		conn, acceptErr := s.listener.Accept()
		if acceptErr != nil {
			return
		}
		go s.session(conn)
	}
}

// session speaks the subset of RFC 5321 this package's transport uses.
func (s *fakeSMTP) session(conn net.Conn) {
	state := &fakeSession{server: s, conn: conn}
	defer state.close()
	if deadlineErr := conn.SetDeadline(time.Now().Add(fakeSessionBudget)); deadlineErr != nil {
		return
	}
	if s.options.has(optImplicitTLS) && !state.upgrade() {
		return
	}
	state.reader = bufio.NewReader(state.conn)
	if !state.write("220 fake.example ESMTP") {
		return
	}
	state.loop()
}

// fakeSession is one connection's mutable state: the socket, whether it has
// been upgraded, and the reader over it.
type fakeSession struct {
	server *fakeSMTP
	conn   net.Conn
	reader *bufio.Reader
	secure bool
}

// close severs the connection at the end of the session.
func (f *fakeSession) close() {
	if closeErr := f.conn.Close(); closeErr != nil {
		return
	}
}

// write emits one CRLF-terminated reply line and reports whether it landed.
func (f *fakeSession) write(line string) bool {
	_, err := f.conn.Write([]byte(line + "\r\n"))
	return err == nil
}

// upgrade wraps the current connection in TLS and rebuilds the reader.
func (f *fakeSession) upgrade() bool {
	tlsConn := tls.Server(f.conn, f.server.tlsConfig)
	if handshakeErr := tlsConn.Handshake(); handshakeErr != nil {
		return false
	}
	f.conn, f.secure = tlsConn, true
	f.reader = bufio.NewReader(f.conn)
	return true
}

// loop reads commands until the client leaves or the socket fails.
func (f *fakeSession) loop() {
	for {
		line, readErr := f.reader.ReadString('\n')
		if readErr != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		f.server.record(line, f.secure)
		if !f.dispatch(strings.ToUpper(line)) {
			return
		}
	}
}

// dispatch answers one command and reports whether the session continues.
func (f *fakeSession) dispatch(upper string) bool {
	switch {
	case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
		return f.greet()
	case upper == "STARTTLS":
		return f.startTLS()
	case strings.HasPrefix(upper, "AUTH"):
		return f.write("235 2.7.0 authentication succeeded")
	case strings.HasPrefix(upper, "MAIL FROM"):
		return f.write("250 2.1.0 ok")
	case strings.HasPrefix(upper, "RCPT TO"):
		return f.rcpt()
	case upper == "DATA":
		return f.data()
	case upper == "QUIT":
		f.write("221 2.0.0 bye")
		return false
	case upper == "NOOP", upper == "RSET":
		return f.write("250 2.0.0 ok")
	default:
		return f.write("500 5.5.1 unrecognised command")
	}
}

// greet emits the multiline 250 response, advertising only what the test asked
// for. The last line carries a space rather than a dash, which is how a client
// knows the list ended (RFC 5321 §4.2.1).
func (f *fakeSession) greet() bool {
	replies := []string{"250-fake.example"}
	if f.server.options.has(optSTARTTLS) && !f.secure {
		replies = append(replies, "250-STARTTLS")
	}
	if f.server.options.has(optAUTH) {
		replies = append(replies, "250-AUTH PLAIN LOGIN")
	}
	for _, reply := range append(replies, "250 HELP") {
		if !f.write(reply) {
			return false
		}
	}
	return true
}

// startTLS answers the upgrade request, refusing it when the test asked for a
// server that never offered it.
func (f *fakeSession) startTLS() bool {
	if !f.server.options.has(optSTARTTLS) {
		return f.write("500 unknown command")
	}
	if !f.write("220 ready to start TLS") {
		return false
	}
	return f.upgrade()
}

// rcpt answers a recipient, refusing it when the test asked for that.
func (f *fakeSession) rcpt() bool {
	if f.server.options.has(optRefuseRcpt) {
		return f.write("550 5.1.1 no such user")
	}
	return f.write("250 2.1.5 ok")
}

// data consumes the message until the lone dot that ends it, then accepts it.
func (f *fakeSession) data() bool {
	if !f.write("354 end with <CRLF>.<CRLF>") {
		return false
	}
	body := []string{}
	for {
		line, readErr := f.reader.ReadString('\n')
		if readErr != nil {
			return false
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			f.server.mutex.Lock()
			f.server.data = body
			f.server.mutex.Unlock()
			return f.write("250 2.0.0 queued")
		}
		body = append(body, line)
	}
}

// record files a command line under the phase it arrived in.
func (s *fakeSMTP) record(line string, secure bool) {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	if secure {
		s.encrypted = append(s.encrypted, line)
		return
	}
	s.clear = append(s.clear, line)
}

// clearLines returns every line the server read over an unencrypted socket.
func (s *fakeSMTP) clearLines() []string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return slices.Clone(s.clear)
}

// allLines returns every line the server read, in both phases.
func (s *fakeSMTP) allLines() []string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return append(slices.Clone(s.clear), s.encrypted...)
}

// message returns the DATA payload of the last accepted message.
func (s *fakeSMTP) message() string {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return strings.Join(s.data, "\r\n")
}

// The self-signed certificate every TLS case uses, generated once per process.
// Ed25519 keeps generation cheap enough to do inside a test.
var (
	testCertOnce sync.Once
	testCertPEM  []byte
	testKeyPEM   []byte
	testCertErr  error
)

// buildTestCert mints a self-signed certificate valid for 127.0.0.1 and
// localhost.
func buildTestCert() {
	public, private, keyErr := ed25519.GenerateKey(nil)
	if keyErr != nil {
		testCertErr = keyErr
		return
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "fake.example"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		DNSNames:              []string{"localhost", "fake.example"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, certErr := x509.CreateCertificate(nil, template, template, public, private)
	if certErr != nil {
		testCertErr = certErr
		return
	}
	keyDER, marshalErr := x509.MarshalPKCS8PrivateKey(private)
	if marshalErr != nil {
		testCertErr = marshalErr
		return
	}
	testCertPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	testKeyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

// testMaterial returns the certificate and key PEM, generating them once.
func testMaterial(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	testCertOnce.Do(buildTestCert)
	if testCertErr != nil {
		t.Fatalf("generate test certificate: %v", testCertErr)
	}
	return testCertPEM, testKeyPEM
}

// serverTLSConfig builds the server half of the test certificate.
func serverTLSConfig(t *testing.T) *tls.Config {
	t.Helper()
	certPEM, keyPEM := testMaterial(t)
	pair, pairErr := tls.X509KeyPair(certPEM, keyPEM)
	if pairErr != nil {
		t.Fatalf("load test key pair: %v", pairErr)
	}
	return &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
}
