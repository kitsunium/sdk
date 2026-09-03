package server_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	stdnet "net"
	"os"
	"testing"
	"time"

	corenet "github.com/kitsunium/sdk/internal/core/net"
	"github.com/kitsunium/sdk/internal/service/net/server"
)

// readBudget is the per-read bound the timeout tests configure. It is short so
// the tests are quick, and comfortably longer than a loopback round trip.
const readBudget time.Duration = 300 * time.Millisecond

// TestReadTimeoutIsPerReadNotPerConnection pins the difference between the two
// readings of ReadTimeout, which is the whole reason the option exists.
//
// A deadline installed once when the connection is accepted is a budget for the
// connection's entire life: a peer that keeps sending, slowly, stays inside it
// until it expires and is then cut off mid-conversation even though it was
// never idle. A per-read bound is the documented promise — each read gets the
// budget afresh — so a peer that answers within the budget every time is never
// cut off, however long the conversation runs.
//
// The test drives more total time than the budget while keeping every single
// read inside it. Under a lifetime deadline that fails; under a per-read one it
// must not.
func TestReadTimeoutIsPerReadNotPerConnection(t *testing.T) {
	t.Parallel()
	const exchanges int = 4
	//: each gap is inside the budget, but they add up past it — which is
	//: exactly what separates the two readings.
	gap := readBudget / 2

	failures := make(chan error, 1)
	srv := server.New()
	srv.Group("echo",
		server.Listen("tcp", "127.0.0.1:0"),
		server.ReadTimeout(readBudget),
	).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		buf := make([]byte, 1)
		//: read once per exchange; every read must succeed.
		for range exchanges {
			if _, err := io.ReadFull(c, buf); err != nil {
				failures <- err
				return err
			}
			if _, err := c.Write(buf); err != nil {
				failures <- err
				return err
			}
		}
		close(failures)
		return nil
	})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	defer closeOrFail(t, c)

	reply := make([]byte, 1)
	for i := range exchanges {
		//: pause inside the per-read budget before each exchange.
		time.Sleep(gap)
		if _, err := c.Write([]byte{byte('a' + i)}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		if err := c.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatalf("set deadline: %v", err)
		}
		if _, err := io.ReadFull(c, reply); err != nil {
			t.Fatalf("exchange %d was cut off after %v of a %v per-read budget — "+
				"the deadline is bounding the connection's life, not each read: %v",
				i, time.Duration(i+1)*gap, readBudget, err)
		}
	}
	//: the handler must have completed every exchange without a timeout.
	if err, failed := <-failures; failed {
		t.Fatalf("handler failed: %v", err)
	}
}

// TestReadTimeoutStillCutsOffASilentPeer is the other half: making the bound
// per-read must not make it toothless. A peer that says nothing at all is still
// cut off, one budget after its last activity.
func TestReadTimeoutStillCutsOffASilentPeer(t *testing.T) {
	t.Parallel()
	failed := make(chan error, 1)
	srv := server.New()
	srv.Group("silent",
		server.Listen("tcp", "127.0.0.1:0"),
		server.ReadTimeout(readBudget),
	).HandleFunc(func(_ context.Context, c corenet.Conn) error {
		buf := make([]byte, 1)
		_, err := io.ReadFull(c, buf)
		failed <- err
		return err
	})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	defer closeOrFail(t, c)

	select {
	case err := <-failed:
		//: a silent peer must trip the deadline, not linger.
		if err == nil {
			t.Fatal("a peer that sent nothing was never cut off")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a peer that sent nothing was still connected long after its %v budget", readBudget)
	}
}

// TestStalledHandshakeIsBounded pins the phase that had no bound at all.
//
// The listener does not negotiate in Accept — the handshake runs on the
// connection's own goroutine, deliberately, so one slow peer cannot stall the
// accept path. The consequence is that a peer which opens a TCP connection to a
// TLS listener and then says nothing was holding a goroutine, an in-flight
// token and a slot under the group's connection ceiling indefinitely: no
// handler had run, so no read or idle deadline applied, and TimeoutsValue's
// Handshake field was read by nothing in the tree and had no option to set it.
func TestStalledHandshakeIsBounded(t *testing.T) {
	t.Parallel()
	reached := make(chan struct{}, 1)
	srv := server.New()
	//: a short explicit budget keeps the test quick; that a group configuring
	//: NOTHING still gets a bound is pinned by TestHandshakeBudgetDefaults.
	srv.Group("tls",
		server.Listen("tcp", "127.0.0.1:0"),
		server.TLS(selfSignedIdentity(t)),
		server.HandshakeTimeout(readBudget),
	).HandleFunc(func(_ context.Context, _ corenet.Conn) error {
		reached <- struct{}{}
		return nil
	})
	if err := srv.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { closeOrFail(t, srv) })

	c, derr := stdnet.DialTimeout("tcp", srv.State().Listeners[0].Address, 2*time.Second)
	if derr != nil {
		t.Fatalf("dial: %v", derr)
	}
	defer closeOrFail(t, c)

	//: say nothing: a ClientHello never arrives, so the negotiation stalls.
	if err := c.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 1)
	_, err := c.Read(buf)
	//: the server must give up on the negotiation and close, which the peer
	//: observes as EOF or a reset rather than as its own deadline expiring.
	if err == nil {
		t.Fatal("the server answered a peer that never sent a ClientHello")
	}
	if errors.Is(err, os.ErrDeadlineExceeded) || isTimeout(err) {
		t.Fatalf("the stalled handshake was never bounded: the peer's own %v "+
			"deadline expired first, so the connection was still held", 5*time.Second)
	}
	//: and no handler may have run for a connection that never negotiated.
	select {
	case <-reached:
		t.Fatal("the handler ran for a connection whose handshake never completed")
	default:
	}
}

// isTimeout reports whether err is a timeout in the net sense.
func isTimeout(err error) bool {
	var netErr stdnet.Error
	//: a deadline the caller set on its own socket reports itself this way.
	if errors.As(err, &netErr) {
		//: only a genuine timeout counts.
		return netErr.Timeout()
	}
	//: anything else is the server closing, which is the outcome under test.
	return false
}

// selfSignedIdentity builds a server TLS identity for the handshake tests.
func selfSignedIdentity(t *testing.T) corenet.IdentityValue {
	t.Helper()
	key, kerr := ecdsa.GenerateKey(elliptic.P256(), nil)
	if kerr != nil {
		t.Fatalf("generate key: %v", kerr)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "kitsunium-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, cerr := x509.CreateCertificate(nil, tmpl, tmpl, &key.PublicKey, key)
	if cerr != nil {
		t.Fatalf("create certificate: %v", cerr)
	}
	keyDER, merr := x509.MarshalECPrivateKey(key)
	if merr != nil {
		t.Fatalf("marshal key: %v", merr)
	}
	id, ierr := corenet.NewIdentityValue(corenet.IdentityParams{
		CertPEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:     pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}),
		MinVersion: tls.VersionTLS12,
	})
	if ierr != nil {
		t.Fatalf("identity: %v", ierr)
	}
	return id
}
